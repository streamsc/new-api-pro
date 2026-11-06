package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/prometheus/common/expfmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestBusinessRouterRejectsMetricsBeforeFrontendFallback(t *testing.T) {
	previous := common.IsMasterNode
	common.IsMasterNode = false
	t.Cleanup(func() { common.IsMasterNode = previous })
	for _, frontend := range []string{"", "https://dashboard.example.test"} {
		t.Run(frontend, func(t *testing.T) {
			t.Setenv("FRONTEND_BASE_URL", frontend)
			business := gin.New()
			SetRouter(business, WebAssets{IndexPage: []byte("dashboard")})
			for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodHead} {
				for _, path := range []string{"/metrics", "/metrics?x=1", "/metrics/", "/metrics/nested"} {
					response := performPluginRequest(business, method, path)
					assert.Equal(t, http.StatusNotFound, response.Code, "%s %s", method, path)
					assert.Empty(t, response.Header().Get("Location"))
					assert.NotContains(t, response.Body.String(), "dashboard")
				}
			}
		})
	}
}

func TestMonitoringRouterServesOnlyMetrics(t *testing.T) {
	previousDB, previousRedis := model.DB, common.RedisEnabled
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	model.DB, common.RedisEnabled = db, false
	t.Cleanup(func() { model.DB, common.RedisEnabled = previousDB, previousRedis; sqlDB.Close() })
	server := httptest.NewServer(NewMetricsRouter())
	defer server.Close()
	for _, path := range []string{"/metrics", "/metrics?x=1"} {
		response, err := server.Client().Get(server.URL + path)
		require.NoError(t, err)
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode)
		var parser expfmt.TextParser
		families, err := parser.TextToMetricFamilies(strings.NewReader(string(body)))
		require.NoError(t, err)
		assert.Contains(t, families, "new_api_concurrency_scrape_success")
	}
	for _, path := range []string{"/", "/metrics/", "/api/status", "/v1/chat/completions", "/debug/pprof/"} {
		response, err := server.Client().Get(server.URL + path)
		require.NoError(t, err)
		response.Body.Close()
		assert.Equal(t, http.StatusNotFound, response.StatusCode, path)
	}
	response, err := server.Client().Post(server.URL+"/metrics", "text/plain", nil)
	require.NoError(t, err)
	response.Body.Close()
	assert.Equal(t, http.StatusNotFound, response.StatusCode)
}
