package operation_setting

import (
	"slices"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
	"golang.org/x/net/http/httpguts"
)

const LogMaxRangeDaysOptionKey = "log_setting.max_range_days"
const LogRequestHeadersEnabledOptionKey = "log_setting.request_headers_enabled"
const LogRequestHeadersExcludedOptionKey = "log_setting.request_headers_excluded"

// LogSetting limits how far regular users can reach into usage logs.
type LogSetting struct {
	RequestHeadersEnabled  bool     `json:"request_headers_enabled"`
	RequestHeadersExcluded []string `json:"request_headers_excluded"`
	// UserExportEnabled lets regular users export their own usage logs.
	// Administrators can always export.
	UserExportEnabled bool `json:"user_export_enabled"`
	// MaxRangeDays is the longest time range a user can search in their logs
	// and dashboard, and the longest range any export can cover.
	MaxRangeDays int `json:"max_range_days"`
}

var logSetting = LogSetting{
	RequestHeadersExcluded: []string{},
	UserExportEnabled:      true,
	MaxRangeDays:           365,
}

// Option updates hold OptionMapRWMutex while replacing these fields. Return a
// detached list so a subsequent config reload cannot change an active capture.
func RequestHeaderLogSettings() (bool, []string) {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	if !logSetting.RequestHeadersEnabled {
		return false, nil
	}
	return true, slices.Clone(logSetting.RequestHeadersExcluded)
}

func ValidateRequestHeaderLogOption(key, value string) error {
	if key == LogRequestHeadersEnabledOptionKey {
		if value != "true" && value != "false" {
			return common.NewMessage("Request header recording must be true or false")
		}
		return nil
	}
	var names []string
	if len(value) > 8192 || common.UnmarshalJsonStr(value, &names) != nil || names == nil || len(names) > 32 {
		return common.NewMessage("Excluded headers must be a JSON array of at most 32 header names")
	}
	for _, name := range names {
		if len(name) > 128 || !httpguts.ValidHeaderFieldName(name) {
			return common.NewMessage("Invalid excluded header name")
		}
	}
	return nil
}

func init() {
	config.GlobalConfig.Register("log_setting", &logSetting)
}

func GetLogSetting() *LogSetting {
	return &logSetting
}

func (s *LogSetting) MaxRangeSeconds() int64 {
	return int64(max(s.MaxRangeDays, 1)) * 24 * 60 * 60
}

func ValidateLogMaxRangeDays(value string) error {
	days, err := strconv.Atoi(value)
	if err != nil || days < 1 {
		return common.NewMessage("The longest log time range must be at least 1 day")
	}
	return nil
}
