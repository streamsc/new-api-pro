package controller

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestProcessChannelErrorUsesSnapshotWithoutLeakingChannelMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousRedisEnabled := common.RedisEnabled
	previousMainDatabaseType := common.MainDatabaseType()
	previousLogDatabaseType := common.LogDatabaseType()
	previousErrorLogEnabled := constant.ErrorLogEnabled

	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&model.User{}, &model.Log{}))
	model.DB, model.LOG_DB = database, database
	common.RedisEnabled = false
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	constant.ErrorLogEnabled = true
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.RedisEnabled = previousRedisEnabled
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		constant.ErrorLogEnabled = previousErrorLogEnabled
		require.NoError(t, sqlDB.Close())
	})

	require.NoError(t, database.Create(&model.User{Id: 7, Username: "log-owner", Group: "default"}).Error)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Set("id", 7)
	ctx.Set("username", "log-owner")
	ctx.Set("token_name", "test-token")
	ctx.Set("token_id", 11)
	ctx.Set("original_model", "gpt-test")
	ctx.Set("group", "default")
	ctx.Set("channel_id", 202)
	ctx.Set("channel_name", "mutable-context-channel")
	ctx.Set("channel_type", 9)
	ctx.Set("use_channel", []string{"101"})
	common.SetContextKey(ctx, constant.ContextKeyRequestStartTime, time.Now().Add(-time.Second))

	channelSnapshot := types.ChannelError{
		ChannelId:   101,
		ChannelType: 1,
		ChannelName: "snapshot-channel",
		AutoBan:     false,
	}
	apiErr := types.NewOpenAIError(errors.New("upstream failed"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)

	processChannelError(ctx, channelSnapshot, apiErr, nil)

	var stored model.Log
	require.NoError(t, database.First(&stored).Error)
	assert.Equal(t, channelSnapshot.ChannelId, stored.ChannelId)
	storedOther, err := common.StrToMap(stored.Other)
	require.NoError(t, err)
	assert.Equal(t, float64(http.StatusBadGateway), storedOther["status_code"])
	for _, key := range []string{"channel_id", "channel_name", "channel_type"} {
		assert.NotContains(t, storedOther, key)
	}
	adminInfo, ok := storedOther["admin_info"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{"101"}, adminInfo["use_channel"])

	logs, total, err := model.GetUserLogs(7, model.LogFilter{Type: model.LogTypeError}, 0, 10)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, logs, 1)
	assert.Equal(t, channelSnapshot.ChannelId, logs[0].ChannelId)
	assert.Empty(t, logs[0].ChannelName)
	userOther, err := common.StrToMap(logs[0].Other)
	require.NoError(t, err)
	assert.NotContains(t, userOther, "admin_info")
	for _, key := range []string{"channel_id", "channel_name", "channel_type"} {
		assert.NotContains(t, userOther, key)
	}
}

// Exercise the ordinary relay entry, then the existing writers and public API.
// A malformed body stops before upstream I/O so headers can be changed deterministically.
func TestUsageUserAgentDatabaseMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousConsume, previousExport := common.LogConsumeEnabled, common.DataExportEnabled
	common.LogConsumeEnabled, common.DataExportEnabled = true, false
	t.Cleanup(func() { common.LogConsumeEnabled, common.DataExportEnabled = previousConsume, previousExport })
	for _, dialect := range []struct {
		kind, env string
		typ       common.DatabaseType
	}{
		{"sqlite", "", common.DatabaseTypeSQLite},
		{"mysql", "AUDIT_MYSQL_DSN", common.DatabaseTypeMySQL},
		{"postgres", "AUDIT_POSTGRES_DSN", common.DatabaseTypePostgreSQL},
	} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip(dialect.env + " is not configured")
			}
			db, _ := newAuditTestDatabase(t, dialect.kind, os.Getenv(dialect.env))
			useLogExportDatabases(t, db, db, dialect.typ, dialect.typ)
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Log{}, &model.Channel{}))
			require.NoError(t, db.Create(&model.User{Id: 7, Username: "ua-owner", Group: "default"}).Error)
			for _, tc := range []struct {
				name, input, want     string
				stream, failure, task bool
			}{
				{name: "success", input: "Client/1.0", want: "Client/1.0"},
				{name: "failure", input: "FailedClient/2", want: "FailedClient/2", failure: true},
				{name: "stream", input: "StreamClient/3", want: "StreamClient/3", stream: true},
				{name: "stream failure", input: "StreamClient/4", want: "StreamClient/4", stream: true, failure: true},
				{name: "empty"},
				{name: "boundary", input: strings.Repeat("a", 1024), want: strings.Repeat("a", 1024)},
				{name: "overlong", input: strings.Repeat("a", 1025), want: strings.Repeat("a", 1024)},
				{name: "unicode boundary", input: strings.Repeat("a", 1022) + "客户端", want: strings.Repeat("a", 1022)},
				{name: "invalid UTF-8", input: "Client/\xff", want: "Client/�"},
				{name: "plain text", input: "<script>alert(1)</script>", want: "<script>alert(1)</script>"},
				{name: "task context is not captured", input: "AsyncClient/1", task: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{"))
					c.Request.Header.Set("Content-Type", "application/json")
					c.Request.Header.Set("User-Agent", tc.input)
					c.Set("username", "ua-owner")
					c.Set(common.RequestIdKey, tc.name)
					if !tc.task {
						Relay(c, types.RelayFormatOpenAI)
					}
					c.Request.Header.Set("User-Agent", "UpstreamSDK/9")
					if tc.failure {
						model.RecordErrorLog(c, 7, 0, "test-model", "", "upstream failed", 0, 0, tc.stream, "default", nil)
					} else {
						model.RecordConsumeLog(c, 7, model.RecordConsumeLogParams{ModelName: "test-model", IsStream: tc.stream})
					}
					logs, total, err := model.GetUserLogs(7, model.LogFilter{RequestId: tc.name}, 0, 10)
					require.NoError(t, err)
					require.EqualValues(t, 1, total)
					require.Len(t, logs, 1)
					rawOther := logs[0].Other
					if rawOther == "" {
						rawOther = "{}"
					}
					other, err := common.StrToMap(rawOther)
					require.NoError(t, err)
					if tc.task {
						assert.NotContains(t, other, "user_agent")
					} else {
						assert.Equal(t, tc.want, other["user_agent"])
					}
					assert.Equal(t, tc.stream, logs[0].IsStream)
					lines := callLogHandler(t, ExportUserLogs, 7, common.RoleCommonUser, "request_id="+strings.ReplaceAll(tc.name, " ", "%20"))
					require.Len(t, lines, 2)
					assert.Equal(t, logs[0].Other, lines[0]["other"])
					lines = callLogHandler(t, ExportAllLogs, 7, common.RoleAdminUser, "request_id="+strings.ReplaceAll(tc.name, " ", "%20"))
					require.Len(t, lines, 2)
					assert.Equal(t, logs[0].Other, lines[0]["other"])
				})
			}
		})
	}
}

func TestUsageUserAgentSurvivesUpstreamHeaderOverride(t *testing.T) {
	for _, tc := range []struct {
		name            string
		stream, failure bool
	}{
		{name: "sync success"}, {name: "sync failure", failure: true},
		{name: "SSE success", stream: true}, {name: "SSE failure", stream: true, failure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newResponsesWSBillingTest(t, `tier("request", fixed(0.002))`, func(*websocket.Conn, *http.Request) {})
			previousErrorLog := constant.ErrorLogEnabled
			constant.ErrorLogEnabled = true
			t.Cleanup(func() { constant.ErrorLogEnabled = previousErrorLog })
			require.NoError(t, model.DB.Model(fixture.channel).Update("header_override", `{"User-Agent":"OverriddenSDK/9"}`).Error)
			fixture.httpUpstream = func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "OverriddenSDK/9", r.UserAgent())
				if tc.failure {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":{"message":"bad request","type":"invalid_request_error","code":"invalid_request"}}`)
					return
				}
				response := `{"id":"resp_ua","object":"response","status":"completed","model":"ws-billing","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\ndata: [DONE]\n\n", response)
				} else {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, response)
				}
			}
			request, err := http.NewRequest(http.MethodPost, fixture.gatewayURL+"/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":"ws-billing","input":"hi","stream":%t}`, tc.stream)))
			require.NoError(t, err)
			request.Header.Set("Authorization", "Bearer sk-"+fixture.token.Key)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("User-Agent", "EntryClient/1")
			response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
			require.NoError(t, err)
			_, err = io.Copy(io.Discard, response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			select {
			case <-fixture.httpDone:
			case <-time.After(3 * time.Second):
				t.Fatal("relay did not finish")
			}
			wantType := model.LogTypeConsume
			if tc.failure {
				wantType = model.LogTypeError
				assert.Equal(t, http.StatusBadRequest, response.StatusCode)
			} else {
				assert.Equal(t, http.StatusOK, response.StatusCode)
			}
			var logs []model.Log
			require.NoError(t, model.LOG_DB.Where("token_id = ?", fixture.token.Id).Find(&logs).Error)
			require.Len(t, logs, 1)
			assert.Equal(t, wantType, logs[0].Type)
			other, err := common.StrToMap(logs[0].Other)
			require.NoError(t, err)
			assert.Equal(t, "EntryClient/1", other["user_agent"])
		})
	}
}
