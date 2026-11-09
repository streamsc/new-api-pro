package router

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListModelsSupportsOpenAIAndGeminiAuthentication(t *testing.T) {
	setupRelayRouterTestDB(t)

	user := model.User{
		Username: "models-user",
		Status:   common.UserStatusEnabled,
		Group:    "default",
		Quota:    100,
	}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		UserId:         user.Id,
		Key:            "modelstestkey",
		Status:         common.TokenStatusEnabled,
		ExpiredTime:    -1,
		UnlimitedQuota: true,
	}).Error)

	engine := gin.New()
	SetRelayRouter(engine)

	tests := []struct {
		name           string
		path           string
		headerName     string
		expectedObject string
		expectedField  string
	}{
		{
			name:           "OpenAI bearer token",
			path:           "/v1/models",
			headerName:     "Authorization",
			expectedObject: "list",
			expectedField:  "data",
		},
		{
			name:          "Gemini API key header",
			path:          "/v1/models",
			headerName:    "x-goog-api-key",
			expectedField: "models",
		},
		{
			name:          "Gemini API key query",
			path:          "/v1/models?key=modelstestkey",
			expectedField: "models",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.headerName != "" {
				value := "modelstestkey"
				if test.headerName == "Authorization" {
					value = "Bearer " + value
				}
				request.Header.Set(test.headerName, value)
			}

			engine.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code)
			var payload map[string]any
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
			assert.Contains(t, payload, test.expectedField)
			assert.NotContains(t, payload, "error")
			if test.expectedObject != "" {
				assert.Equal(t, test.expectedObject, payload["object"])
			}
		})
	}
}

func setupRelayRouterTestDB(t *testing.T) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	originalIsMasterNode := common.IsMasterNode
	originalRedisEnabled := common.RedisEnabled
	originalSQLitePath := common.SQLitePath
	originalMainDatabaseType := common.MainDatabaseType()
	originalLogDatabaseType := common.LogDatabaseType()
	originalSQLDSN, hadSQLDSN := os.LookupEnv("SQL_DSN")

	common.IsMasterNode = false
	common.RedisEnabled = false
	common.SQLitePath = fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, os.Setenv("SQL_DSN", "local"))
	require.NoError(t, model.InitDB())
	model.LOG_DB = model.DB
	require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.Token{}, &model.Ability{}))

	t.Cleanup(func() {
		if sqlDB, err := model.DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		common.IsMasterNode = originalIsMasterNode
		common.RedisEnabled = originalRedisEnabled
		common.SQLitePath = originalSQLitePath
		common.SetDatabaseTypes(originalMainDatabaseType, originalLogDatabaseType)
		if hadSQLDSN {
			require.NoError(t, os.Setenv("SQL_DSN", originalSQLDSN))
		} else {
			require.NoError(t, os.Unsetenv("SQL_DSN"))
		}
	})
}

func TestInboundHeaderSnapshotCoversProductionModelRoutes(t *testing.T) {
	setupRelayRouterTestDB(t)
	settings := operation_setting.GetLogSetting()
	previous := *settings
	settings.RequestHeadersEnabled = true
	settings.RequestHeadersExcluded = nil
	t.Cleanup(func() { *settings = previous })
	var snapshot, matched, encoding string
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Next()
		snapshot, matched, encoding = c.GetString(model.UsageRequestHeadersKey), c.FullPath(), c.GetHeader("Content-Encoding")
	})
	SetRelayRouter(engine)
	SetTaskPluginProtocolRouter(engine)
	SetTaskRouter(engine)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, err := writer.Write([]byte(`{"model":"unused"}`))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	for _, path := range []string{
		"/v1/messages", "/v1/completions", "/v1/chat/completions", "/v1/responses", "/v1/responses/compact",
		"/v1/alpha/search", "/v1/tools/search", "/v1/tools/fetch", "/v1/images/generations", "/v1/images/edits",
		"/v1/edits", "/v1/embeddings", "/v1/audio/transcriptions", "/v1/audio/translations", "/v1/audio/speech",
		"/v1/rerank", "/v1/engines/test/embeddings", "/v1/models/test:generateContent", "/v1/moderations",
		"/v1beta/models/test:generateContent", "/mj/submit/imagine", "/fast/mj/submit/blend", "/v1/tasks/test", "/v1/videos", "/pg/chat/completions",
	} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(compressed.Bytes()))
			request.Header.Set("Content-Encoding", "gzip")
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Trace", "inbound-route-canary")
			engine.ServeHTTP(httptest.NewRecorder(), request)
			require.NotEmpty(t, matched, "must exercise a registered production route")
			assert.Empty(t, encoding, "decompression has mutated the live header")
			assert.Contains(t, snapshot, `"content-encoding":["gzip"]`)
			assert.Contains(t, snapshot, "inbound-route-canary")
		})
	}
	for _, path := range []string{"/v1/responses", "/v1/realtime"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Upgrade", "websocket")
		request.Header.Set("Sec-WebSocket-Protocol", "openai-insecure-api-key.")
		engine.ServeHTTP(httptest.NewRecorder(), request)
		require.NotEmpty(t, matched)
		assert.Contains(t, snapshot, `"source":"websocket_handshake"`)
		assert.NotContains(t, snapshot, "sec-websocket-protocol")
	}
}
