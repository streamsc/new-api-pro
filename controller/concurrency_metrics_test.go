package controller

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/concurrencymetrics"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetricsHTTPCurrentSnapshotAndKeepAlive(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	previous := common.RedisEnabled
	common.RedisEnabled = false
	defer func() { common.RedisEnabled = previous }()
	good, broken := `{"max_concurrency":10}`, "{broken"
	require.NoError(t, db.Create(&model.Channel{Id: 801, Status: 2, Setting: &good}).Error)
	require.NoError(t, db.Create(&model.Channel{Id: 802, Setting: &broken}).Error)
	router := gin.New()
	router.GET("/metrics", NewConcurrencyMetricsHandler())
	router.GET("/next", func(c *gin.Context) { c.String(200, "next") })
	server := httptest.NewServer(router)
	defer server.Close()
	client := server.Client()
	response, err := client.Get(server.URL + "/metrics")
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, 200, response.StatusCode)
	assert.Contains(t, response.Header.Get("Content-Type"), "text/plain")
	assert.Contains(t, string(body), `new_api_channel_concurrency_limit{channel_id="801",scope="process"} 10`)
	assert.Contains(t, string(body), `new_api_channel_concurrency_in_flight{channel_id="802",scope="process"} 0`)
	assert.Contains(t, string(body), `new_api_concurrency_scrape_success{component="channel_config",scope="process"} 0`)
	assert.NotContains(t, string(body), broken)
	var saved model.Channel
	require.NoError(t, db.First(&saved, 802).Error)
	require.NotNil(t, saved.Setting)
	assert.Equal(t, broken, *saved.Setting)
	reused := false
	req, err := http.NewRequest(http.MethodGet, server.URL+"/next", nil)
	require.NoError(t, err)
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }}))
	response, err = client.Do(req)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	response.Body.Close()
	assert.True(t, reused, "metrics must leave the connection usable")
	first, _, err := service.AcquireChannelConcurrency(context.Background(), 801, 10, "metrics-first")
	require.NoError(t, err)
	defer first.Release()
	second, _, err := service.AcquireChannelConcurrency(context.Background(), 801, 10, "metrics-second")
	require.NoError(t, err)
	defer second.Release()
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 801).Update("setting", `{"max_concurrency":1}`).Error)
	response, err = client.Get(server.URL + "/metrics")
	require.NoError(t, err)
	body, err = io.ReadAll(response.Body)
	require.NoError(t, err)
	response.Body.Close()
	assert.Contains(t, string(body), `new_api_channel_concurrency_limit{channel_id="801",scope="process"} 1`)
	assert.Contains(t, string(body), `new_api_channel_concurrency_in_flight{channel_id="801",scope="process"} 2`)
	assert.NoError(t, first.Context().Err())
	assert.NoError(t, second.Context().Err())
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 801).Update("setting", "{}").Error)
	require.NoError(t, db.Delete(&model.Channel{}, 802).Error)
	response, err = client.Get(server.URL + "/metrics")
	require.NoError(t, err)
	body, err = io.ReadAll(response.Body)
	require.NoError(t, err)
	response.Body.Close()
	assert.NotContains(t, string(body), "channel_id=\"802\"")
	assert.NotContains(t, string(body), "new_api_channel_concurrency_limit{")
	assert.Contains(t, string(body), `new_api_channel_concurrency_limit_enabled{channel_id="801",scope="process"} 0`)
}

func TestMetricsHTTPConcurrencyAndCancellation(t *testing.T) {
	entered, exited := make(chan struct{}, 2), make(chan struct{}, 2)
	h := &concurrencyMetricsHandler{slots: make(chan struct{}, 2), writeTimeout: 8 * time.Second, collect: func(ctx context.Context) concurrencymetrics.Snapshot {
		entered <- struct{}{}
		<-ctx.Done()
		exited <- struct{}{}
		return concurrencymetrics.Snapshot{Scope: "process"}
	}}
	router := gin.New()
	router.GET("/metrics", h.serve)
	server := httptest.NewServer(router)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{}, 2)
	for range 2 {
		go func() {
			defer func() { finished <- struct{}{} }()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/metrics", nil)
			resp, err := server.Client().Do(req)
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("collector not entered")
		}
	}
	resp, err := server.Client().Get(server.URL + "/metrics")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 503, resp.StatusCode)
	cancel()
	for range 2 {
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Fatal("cancellation did not reach collector")
		}
		<-finished
	}
	// Acquiring both slots waits for the handlers' defers, without a timing assertion.
	for range 2 {
		select {
		case h.slots <- struct{}{}:
		case <-time.After(5 * time.Second):
			t.Fatal("slot was not released")
		}
	}
	for range 2 {
		<-h.slots
	}
}

func TestMetricsHTTPRejectsUnsupportedDeadline(t *testing.T) {
	h := &concurrencyMetricsHandler{slots: make(chan struct{}, 2), writeTimeout: time.Second, collect: func(context.Context) concurrencymetrics.Snapshot {
		t.Fatal("must not collect without deadline")
		return concurrencymetrics.Snapshot{}
	}}
	router := gin.New()
	router.GET("/metrics", h.serve)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	assert.Equal(t, 500, w.Code)
	assert.Equal(t, "metrics write deadline unavailable\n", w.Body.String())
}

type metricsObservedWriter struct {
	http.ResponseWriter
	writeErrors chan error
}

func (w metricsObservedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w metricsObservedWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if err != nil {
		select {
		case w.writeErrors <- err:
		default:
		}
	}
	return n, err
}

func TestMetricsHTTPSlowReaderReleasesSlot(t *testing.T) {
	value := 1
	samples := make([]concurrencymetrics.ChannelSample, 2000)
	for i := range samples {
		samples[i] = concurrencymetrics.ChannelSample{ChannelID: i + 1, InFlight: &value, Limit: &value}
	}
	h := &concurrencyMetricsHandler{slots: make(chan struct{}, 2), writeTimeout: 500 * time.Millisecond, collect: func(context.Context) concurrencymetrics.Snapshot {
		return concurrencymetrics.Snapshot{Scope: "process", Channels: samples}
	}}
	router := gin.New()
	router.GET("/metrics", h.serve)
	errors := make(chan error, 1)
	done := make(chan struct{}, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { done <- struct{}{} }()
		router.ServeHTTP(metricsObservedWriter{w, errors}, r)
	}))
	server.Config.ConnState = func(conn net.Conn, state http.ConnState) {
		if state == http.StateNew {
			_ = conn.(*net.TCPConn).SetWriteBuffer(1024)
		}
	}
	server.Start()
	defer server.Close()
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.(*net.TCPConn).SetReadBuffer(1024))
	_, err = io.WriteString(conn, "GET /metrics HTTP/1.1\r\nHost: localhost\r\nAccept-Encoding: identity\r\n\r\n")
	require.NoError(t, err)
	select {
	case err := <-errors:
		assert.ErrorIs(t, err, os.ErrDeadlineExceeded)
	case <-time.After(5 * time.Second):
		t.Fatal("slow reader did not hit write deadline")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not exit")
	}
	assert.Empty(t, h.slots)
}

func TestMetricsHTTPDependencyFailure(t *testing.T) {
	h := &concurrencyMetricsHandler{slots: make(chan struct{}, 2), writeTimeout: time.Second, collect: func(context.Context) concurrencymetrics.Snapshot { return concurrencymetrics.Snapshot{Scope: "redis"} }}
	router := gin.New()
	router.GET("/metrics", h.serve)
	server := httptest.NewServer(router)
	defer server.Close()
	resp, err := server.Client().Get(server.URL + "/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, string(body), `component="concurrency",scope="redis"} 0`)
	assert.False(t, strings.Contains(string(body), "new_api_channel_concurrency_in_flight"))
}
