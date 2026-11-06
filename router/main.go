package router

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"

	"github.com/gin-gonic/gin"
)

func SetRouter(router *gin.Engine, assets WebAssets) {
	// Explicit tombstones avoid Gin's trailing-slash redirects and prevent
	// plugin or frontend fallbacks from serving the old monitoring path.
	notFound := func(c *gin.Context) { c.AbortWithStatus(http.StatusNotFound) }
	router.Any("/metrics", notFound)
	router.Any("/metrics/*path", notFound)
	SetApiRouter(router)
	SetDashboardRouter(router)
	SetRelayRouter(router)
	SetTaskPluginProtocolRouter(router)
	SetVideoRouter(router)
	SetTaskRouter(router)
	pluginDispatcher := SetPluginRouter(router)
	frontendBaseUrl := os.Getenv("FRONTEND_BASE_URL")
	if common.IsMasterNode && frontendBaseUrl != "" {
		frontendBaseUrl = ""
		common.SysLog(common.LogText("FRONTEND_BASE_URL is ignored on master node"))
	}
	if frontendBaseUrl == "" {
		SetWebRouter(router, assets, pluginDispatcher)
	} else {
		frontendBaseUrl = strings.TrimSuffix(frontendBaseUrl, "/")
		router.NoRoute(
			pluginDispatcher,
			middleware.RouteTag("web"),
			middleware.AccessTokenAudit(),
			func(c *gin.Context) {
				c.Redirect(http.StatusMovedPermanently, fmt.Sprintf("%s%s", frontendBaseUrl, c.Request.RequestURI))
			},
		)
	}
}

// NewMetricsRouter serves only the private monitoring endpoint, independently
// of business authentication, rate limits, plugins and frontend redirects.
func NewMetricsRouter() *gin.Engine {
	router := gin.New()
	router.Use(gin.Recovery())
	router.RedirectTrailingSlash = false
	router.GET("/metrics", controller.NewConcurrencyMetricsHandler())
	return router
}
