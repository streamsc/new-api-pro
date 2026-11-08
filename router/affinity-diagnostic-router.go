package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-gonic/gin"
)

func registerAffinityDiagnosticRoutes(api *gin.RouterGroup) {
	diagnostics := api.Group("/affinity-diagnostics", middleware.DisableCache(), middleware.AdminAuth())
	diagnostics.GET("", controller.ListAffinityDiagnostics)
	diagnostics.POST("", controller.CreateAffinityDiagnostic)
	diagnostics.POST("/:task/stop", controller.StopAffinityDiagnostic)
	diagnostics.DELETE("/:task", controller.DeleteAffinityDiagnostic)
	diagnostics.GET("/:task/samples", controller.ListAffinityDiagnosticSamples)
	diagnostics.GET("/:task/samples/:sample", controller.GetAffinityDiagnosticSample)
	diagnostics.POST("/:task/samples/:sample/extract", controller.ExtractAffinityDiagnosticSample)
}
