package model

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func enableRequestHeaderLogs(t testing.TB) {
	t.Helper()
	setting := operation_setting.GetLogSetting()
	previous := *setting
	setting.RequestHeadersEnabled = true
	setting.RequestHeadersExcluded = []string{"x-company-secret"}
	t.Cleanup(func() { *setting = previous })
}

func TestUsageRequestHeadersFilterAndFreeze(t *testing.T) {
	enableRequestHeaderLogs(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "http://gateway.example/v1/responses", strings.NewReader("unread body"))
	c.Request.Header = http.Header{
		"x-Trace": {"first", "second"}, "X-TRACE": {"third"},
		"AUTHORIZATION": {"bearer-canary"}, "Cookie": {"cookie-canary"},
		"X-Company-Secret": {"custom-canary"}, "Host": {"fake-host"},
		"Upgrade": {"websocket"}, "Sec-WebSocket-Protocol": {"openai-insecure-api-key.ws-canary"},
	}
	for _, name := range []string{"Proxy-Authorization", "Set-Cookie", "X-API-Key", "API-Key", "ApiKey", "X-Goog-API-Key", "MJ-API-Secret", "X-Auth-Token", "X-Access-Token"} {
		c.Request.Header[name] = []string{"credential-canary"}
	}
	CaptureUsageRequestHeaders(c)
	raw := c.GetString(UsageRequestHeadersKey)
	var snapshot usageRequestHeaders
	require.NoError(t, common.UnmarshalJsonStr(raw, &snapshot))
	assert.Equal(t, "websocket_handshake", snapshot.Source)
	assert.Equal(t, []string{"third", "first", "second"}, snapshot.Headers["x-trace"])
	assert.Equal(t, []string{"gateway.example"}, snapshot.Headers["host"])
	assert.False(t, snapshot.Incomplete)
	assert.NotContains(t, raw, "canary")
	assert.Equal(t, []string{"bearer-canary"}, c.Request.Header["AUTHORIZATION"])
	c.Request.Header["x-Trace"][0] = "rewritten"
	c.Request.Host = "upstream.example"
	CaptureUsageRequestHeaders(c)
	assert.Equal(t, raw, c.GetString(UsageRequestHeadersKey))
	remaining := make([]byte, len("unread body"))
	_, err := c.Request.Body.Read(remaining)
	require.NoError(t, err)
	assert.Equal(t, "unread body", string(remaining))

	other := NewLogOther()
	other.SetAdmin("existing", true)
	stored := usageLogOtherJSON(c, other)
	assert.Contains(t, stored, "request_headers")
	assert.NotContains(t, other.JSONString(), "request_headers", "caller metadata may be printed or reused")
	assert.NotContains(t, formatLogOtherJSON(stored, logOtherVisibilityUser), "request_headers")
	assert.Contains(t, formatLogOtherJSON(stored, logOtherVisibilityAdmin), "request_headers")
	assert.Contains(t, formatLogOtherJSON(stored, logOtherVisibilityRoot), "request_headers")
}

func TestUsageRequestHeadersLimitsAreDeterministic(t *testing.T) {
	enableRequestHeaderLogs(t)
	for _, tc := range []struct {
		name    string
		headers http.Header
		reason  string
		count   int
	}{
		{"field bytes", http.Header{"X-Large": {strings.Repeat("a", 1025)}, "Z-Small": {"kept"}}, "field_bytes", 1},
		{"UTF-8", http.Header{"X-Bad": {"\xff"}, "Z-Small": {"客户端"}}, "invalid", 1},
		{"invalid name", http.Header{"bad name": {"skip"}, "Z-Small": {"kept"}}, "invalid", 1},
		{"long name", http.Header{strings.Repeat("x", 129): {"skip"}, "Z-Small": {"kept"}}, "invalid", 1},
		{"values", http.Header{"X-Many": make([]string, 17), "Z-Small": {"kept"}}, "value_count", 1},
		{"escaping", http.Header{"A": {strings.Repeat("<", 1024)}, "B": {strings.Repeat("<", 1024)}, "Z-Small": {"kept"}}, "total_bytes", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Request.Host = ""
			c.Request.Header = tc.headers
			CaptureUsageRequestHeaders(c)
			first := c.GetString(UsageRequestHeadersKey)
			var snapshot usageRequestHeaders
			require.NoError(t, common.UnmarshalJsonStr(first, &snapshot))
			assert.True(t, snapshot.Incomplete)
			assert.Equal(t, tc.count, snapshot.Skipped[tc.reason])
			assert.Contains(t, snapshot.Headers, "z-small")
			assert.LessOrEqual(t, len(first), 8192)
			c.Keys = nil
			CaptureUsageRequestHeaders(c)
			assert.Equal(t, first, c.GetString(UsageRequestHeadersKey))
		})
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Host = ""
	for i := range 70 {
		c.Request.Header.Set(fmt.Sprintf("X-%02d", i), "value")
	}
	CaptureUsageRequestHeaders(c)
	var snapshot usageRequestHeaders
	require.NoError(t, common.UnmarshalJsonStr(c.GetString(UsageRequestHeadersKey), &snapshot))
	assert.Len(t, snapshot.Headers, 64)
	assert.Equal(t, 6, snapshot.Skipped["field_count"])
	assert.Contains(t, snapshot.Headers, "x-63")
	assert.NotContains(t, snapshot.Headers, "x-64")
}

func TestUsageRequestHeadersDisabledAndHostExcluded(t *testing.T) {
	enableRequestHeaderLogs(t)
	setting := operation_setting.GetLogSetting()
	setting.RequestHeadersEnabled = false
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	CaptureUsageRequestHeaders(c)
	_, marked := c.Get(UsageRequestHeadersKey)
	assert.True(t, marked)
	setting.RequestHeadersEnabled = true
	CaptureUsageRequestHeaders(c)
	assert.Empty(t, c.GetString(UsageRequestHeadersKey))
	c.Keys = nil
	setting.RequestHeadersExcluded = []string{"HOST"}
	CaptureUsageRequestHeaders(c)
	assert.NotContains(t, c.GetString(UsageRequestHeadersKey), "example.com")
}

func TestRequestHeaderLogOptionsRejectInvalidInput(t *testing.T) {
	for _, value := range []string{`null`, `"header"`, `["bad name"]`, `["x\r\nheader"]`, `["` + strings.Repeat("x", 129) + `"]`, `[` + strings.Repeat(`"x",`, 32) + `"y"]`} {
		assert.Error(t, validateOptionValue(operation_setting.LogRequestHeadersExcludedOptionKey, value), value)
	}
	assert.Error(t, validateOptionValue(operation_setting.LogRequestHeadersEnabledOptionKey, "1"))
	assert.NoError(t, validateOptionValue(operation_setting.LogRequestHeadersEnabledOptionKey, "false"))
	assert.NoError(t, validateOptionValue(operation_setting.LogRequestHeadersExcludedOptionKey, `["X-Custom","host"]`))
	assert.NoError(t, validateOptionValue(operation_setting.LogRequestHeadersExcludedOptionKey, `[]`))
}

func TestUsageRequestHeadersDoNotLeakThroughDebugSQL(t *testing.T) {
	enableRequestHeaderLogs(t)
	previousLogDB, previousDebug := LOG_DB, common.DebugEnabled
	t.Cleanup(func() { LOG_DB, common.DebugEnabled = previousLogDB, previousDebug })
	common.DebugEnabled = true
	var output bytes.Buffer
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: newGormLogger(&output)})
	require.NoError(t, err)
	connection, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	LOG_DB = db.Debug()
	require.NoError(t, LOG_DB.AutoMigrate(&Log{}))
	output.Reset()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("X-Trace", "private-header-canary")
	CaptureUsageRequestHeaders(c)
	log := &Log{Other: usageLogOtherJSON(c, nil)}
	require.NoError(t, createUsageLog(c, log))
	assert.NotContains(t, output.String(), "private-header-canary")
	require.NoError(t, LOG_DB.Migrator().DropTable(&Log{}))
	output.Reset()
	err = createUsageLog(c, log)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "private-header-canary")
	assert.Empty(t, output.String())
}

func BenchmarkUsageRequestHeaders(b *testing.B) {
	enableRequestHeaderLogs(b)
	for _, tc := range []struct {
		name        string
		enabled     bool
		count, size int
	}{
		{"disabled", false, 8, 256}, {"typical_2KiB", true, 8, 256}, {"bounded_8KiB", true, 16, 1024},
	} {
		b.Run(tc.name, func(b *testing.B) {
			operation_setting.GetLogSetting().RequestHeadersEnabled = tc.enabled
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			for i := range tc.count {
				c.Request.Header.Set(fmt.Sprintf("X-%02d", i), strings.Repeat("v", tc.size))
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				c.Keys = nil
				CaptureUsageRequestHeaders(c)
			}
		})
	}
}
