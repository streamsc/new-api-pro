package model

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"golang.org/x/net/http/httpguts"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// UsageRequestHeadersKey holds immutable JSON, including an empty marker when
// recording is disabled. Responses WS carries it into each response.create.
const UsageRequestHeadersKey = "usage_request_headers"

type usageRequestHeaders struct {
	Source     string              `json:"source"`
	Headers    map[string][]string `json:"headers"`
	Incomplete bool                `json:"incomplete"`
	Skipped    map[string]int      `json:"skipped"`
}

// CaptureUsageRequestHeaders must run before decompression, authentication and
// channel overrides. It never reads the body or retains the original headers.
func CaptureUsageRequestHeaders(c *gin.Context) {
	if _, observed := c.Get(UsageRequestHeadersKey); observed {
		return
	}
	c.Set(UsageRequestHeadersKey, "")
	enabled, excluded := operation_setting.RequestHeaderLogSettings()
	if !enabled {
		return
	}
	snapshot := usageRequestHeaders{
		Source: "http", Headers: make(map[string][]string),
		Skipped: map[string]int{"field_count": 0, "field_bytes": 0, "value_count": 0, "invalid": 0, "total_bytes": 0},
	}
	if strings.EqualFold(c.GetHeader("Upgrade"), "websocket") {
		snapshot.Source = "websocket_handshake"
	}
	// Normalize before sorting, merge case variants deterministically, and
	// filter before considering limits. Host lives outside Go's Header map.
	names := make([]string, 0, len(c.Request.Header))
	for name := range c.Request.Header {
		names = append(names, name)
	}
	slices.Sort(names)
	fields := make(map[string][]string, len(names)+1)
	for _, name := range names {
		if len(name) > 128 || !httpguts.ValidHeaderFieldName(name) {
			snapshot.Skipped["invalid"]++
			continue
		}
		nameLower := strings.ToLower(name)
		if nameLower == "host" || common.ExcludeCredentialHeader(nameLower, excluded) {
			continue
		}
		values := c.Request.Header[name]
		if prior, exists := fields[nameLower]; exists {
			// Keep only enough references to identify an over-limit field.
			fields[nameLower] = append(slices.Clip(prior[:min(len(prior), 17)]), values[:min(len(values), 17)]...)
		} else {
			fields[nameLower] = values
		}
	}
	if c.Request.Host != "" && !common.ExcludeCredentialHeader("host", excluded) {
		fields["host"] = []string{c.Request.Host}
	}
	names = names[:0]
	for name := range fields {
		names = append(names, name)
	}
	slices.Sort(names)
	// Reserve the exact worst-case metadata size. Count each entry's actual
	// JSON bytes (including escaping); avoid re-marshalling an ever-growing map.
	invalidNames := snapshot.Skipped["invalid"]
	for reason := range snapshot.Skipped {
		snapshot.Skipped[reason] = len(c.Request.Header) + 1
	}
	snapshot.Incomplete = true
	metadata, err := common.Marshal(snapshot)
	if err != nil {
		return
	}
	for reason := range snapshot.Skipped {
		snapshot.Skipped[reason] = 0
	}
	snapshot.Skipped["invalid"] = invalidNames
	used := len(metadata) + 1 // false is one byte longer than true
	for _, name := range names {
		values := fields[name]
		if len(values) > 16 {
			snapshot.Skipped["value_count"]++
			continue
		}
		size, valid := 0, true
		for _, value := range values {
			size += len(value)
			if size > 1024 {
				break
			}
			valid = valid && utf8.ValidString(value)
		}
		if size > 1024 {
			snapshot.Skipped["field_bytes"]++
			continue
		}
		if !valid {
			snapshot.Skipped["invalid"]++
			continue
		}
		if len(snapshot.Headers) >= 64 {
			snapshot.Skipped["field_count"]++
			continue
		}
		if values == nil {
			values = []string{}
		}
		entry, err := common.Marshal(map[string][]string{name: values})
		if err != nil {
			snapshot.Skipped["invalid"]++
			continue
		}
		entryBytes := len(entry) - 2
		if len(snapshot.Headers) > 0 {
			entryBytes++
		}
		if used+entryBytes > 8192 {
			snapshot.Skipped["total_bytes"]++
			continue
		}
		used += entryBytes
		snapshot.Headers[name] = values
	}
	snapshot.Incomplete = false
	for _, count := range snapshot.Skipped {
		snapshot.Incomplete = snapshot.Incomplete || count > 0
	}
	encoded, err := common.Marshal(snapshot)
	if err != nil || len(encoded) > 8192 {
		// No header content or encoder errors are printed to runtime logs.
		c.Set(UsageRequestHeadersKey, `{"source":"`+snapshot.Source+`","headers":{},"incomplete":true,"skipped":{"serialization":1}}`)
		return
	}
	c.Set(UsageRequestHeadersKey, string(encoded))
}

func usageLogOtherJSON(c *gin.Context, other *LogOther) string {
	snapshot := c.GetString(UsageRequestHeadersKey)
	if snapshot == "" {
		return other.JSONString()
	}
	// toMap detaches privileged maps. Never attach headers to params.Other:
	// a caller may reuse it and the consume logger prints those parameters.
	values := other.toMap()
	admin, _ := values[logOtherAdminInfoKey].(map[string]any)
	if admin == nil {
		admin = make(map[string]any)
	}
	admin["request_headers"] = common.RawMessage(snapshot)
	values[logOtherAdminInfoKey] = admin
	encoded, err := common.Marshal(values)
	if err != nil {
		return other.JSONString()
	}
	return string(encoded)
}

func createUsageLog(c *gin.Context, log *Log) error {
	if c.GetString(UsageRequestHeadersKey) == "" {
		return createLog(log)
	}
	ensureLogRequestId(log)
	// DEBUG SQL can inline the entire other column. Suppress SQL only for
	// these writes, and return error codes without database-supplied values.
	err := LOG_DB.Session(&gorm.Session{Logger: logger.Discard}).Create(log).Error
	if err == nil {
		return nil
	}
	if sanitized := sanitizeDBError(err); sanitized != err {
		return sanitized
	}
	return fmt.Errorf("usage log write failed (%T)", err)
}
