package controller

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/pkg/concurrencymetrics"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type concurrencyMetricsHandler struct {
	slots        chan struct{}
	collect      func(context.Context) concurrencymetrics.Snapshot
	writeTimeout time.Duration
}

func NewConcurrencyMetricsHandler() gin.HandlerFunc {
	handler := &concurrencyMetricsHandler{slots: make(chan struct{}, 2), collect: service.CollectChannelConcurrency, writeTimeout: 8 * time.Second}
	return handler.serve
}

func (h *concurrencyMetricsHandler) serve(c *gin.Context) {
	started := time.Now()
	c.Set(middleware.RouteTagKey, "metrics")
	if c.Request.Context().Err() != nil {
		return
	}
	response := http.NewResponseController(c.Writer)
	if err := response.SetWriteDeadline(started.Add(h.writeTimeout)); err != nil {
		http.Error(c.Writer, "metrics write deadline unavailable", http.StatusInternalServerError)
		return
	}
	defer func() {
		if err := response.SetWriteDeadline(time.Time{}); err != nil {
			logger.LogError(c.Request.Context(), "failed to clear metrics write deadline")
		}
	}()
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		http.Error(c.Writer, "metrics concurrent request limit reached", http.StatusServiceUnavailable)
		_ = response.Flush()
		return
	}
	ctx, cancel := context.WithDeadline(c.Request.Context(), started.Add(5*time.Second))
	defer cancel()
	snapshot := h.collect(ctx)
	if c.Request.Context().Err() != nil {
		return
	}
	registry := prometheus.NewRegistry()
	if err := registry.Register(concurrencymetrics.Collector{Snapshot: snapshot}); err != nil {
		http.Error(c.Writer, "metrics registry failed", http.StatusInternalServerError)
		_ = response.Flush()
		return
	}
	promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		ErrorHandling: promhttp.HTTPErrorOnError,
		ErrorLog:      metricsErrorLogger{ctx: c.Request.Context()},
	}).ServeHTTP(c.Writer, c.Request)
	// Flush while the route deadline is still active, including net/http's buffer.
	if err := response.Flush(); err != nil {
		logger.LogError(c.Request.Context(), "failed to flush metrics response")
	}
}

type metricsErrorLogger struct{ ctx context.Context }

func (l metricsErrorLogger) Println(values ...interface{}) {
	logger.LogError(l.ctx, fmt.Sprint(values...))
}
