package relaymetrics

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogicalResults(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name, result              string
		stream                    bool
		response                  relaycommon.ResponseOutcome
		reason                    relaycommon.StreamEndReason
		incomplete                string
		terminal, business, panic bool
		ctx                       context.Context
		apiErr                    *types.NewAPIError
		endErr                    error
	}{
		{name: "sync", result: "success"},
		{name: "sync error", apiErr: types.NewError(errors.New("private"), types.ErrorCodeBadResponse), result: "failure"},
		{name: "business ignored is failure", business: true, result: "failure"},
		{name: "known business rejection before late cancel", business: true, ctx: cancelled, result: "failure"},
		{name: "cancelled sync", ctx: cancelled, apiErr: types.NewError(context.Canceled, types.ErrorCodeBadResponse), result: "cancelled"},
		{name: "sync handler completed before late cancel", ctx: cancelled, result: "success"},
		{name: "internal cancel", apiErr: types.NewError(context.Canceled, types.ErrorCodeBadResponse), result: "failure"},
		{name: "panic", panic: true, result: "failure"},
		{name: "completed", stream: true, response: relaycommon.ResponseOutcomeCompleted, reason: relaycommon.StreamEndReasonEOF, result: "success"},
		{name: "late cancel", ctx: cancelled, stream: true, response: relaycommon.ResponseOutcomeCompleted, reason: relaycommon.StreamEndReasonDone, result: "success"},
		{name: "known failure after 200", stream: true, response: relaycommon.ResponseOutcomeFailed, reason: relaycommon.StreamEndReasonEOF, result: "failure"},
		{name: "done", stream: true, reason: relaycommon.StreamEndReasonDone, result: "success"},
		{name: "missing required terminal", stream: true, terminal: true, reason: relaycommon.StreamEndReasonEOF, result: "failure"},
		{name: "legacy eof", stream: true, reason: relaycommon.StreamEndReasonEOF, result: "unknown"},
		{name: "handler stop", stream: true, reason: relaycommon.StreamEndReasonHandlerStop, result: "unknown"},
		{name: "client disconnect", stream: true, ctx: cancelled, reason: relaycommon.StreamEndReasonClientGone, result: "cancelled"},
		{name: "unconfirmed disconnect", stream: true, reason: relaycommon.StreamEndReasonClientGone, result: "unknown"},
		{name: "upstream cancelled", stream: true, response: relaycommon.ResponseOutcomeCancelled, result: "unknown"},
		{name: "timeout", stream: true, reason: relaycommon.StreamEndReasonTimeout, result: "failure"},
		{name: "scanner error", stream: true, reason: relaycommon.StreamEndReasonScannerErr, result: "failure"},
		{name: "completed with transport error", stream: true, response: relaycommon.ResponseOutcomeCompleted, reason: relaycommon.StreamEndReasonEOF, endErr: errors.New("transport"), result: "failure"},
		{name: "token limit", stream: true, response: relaycommon.ResponseOutcomeIncomplete, incomplete: "max_output_tokens", result: "success"},
		{name: "safety", stream: true, response: relaycommon.ResponseOutcomeIncomplete, incomplete: "safety", result: "failure"},
		{name: "unknown incomplete", stream: true, response: relaycommon.ResponseOutcomeIncomplete, incomplete: "new_reason", result: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{IsStream: tc.stream, PerformanceBusinessRejection: tc.business}
			if tc.stream {
				s := relaycommon.NewStreamStatus()
				info.StreamStatus = s
				s.SetEndReason(tc.reason, tc.endErr)
				if tc.terminal {
					s.RequireTerminal()
				}
				switch tc.response {
				case relaycommon.ResponseOutcomeCompleted:
					s.MarkCompleted()
				case relaycommon.ResponseOutcomeFailed:
					s.MarkFailed("private", "private", 500)
				case relaycommon.ResponseOutcomeIncomplete:
					s.MarkIncomplete(tc.incomplete)
				case relaycommon.ResponseOutcomeCancelled:
					s.MarkCancelled()
				}
			}
			assert.Equal(t, tc.result, Classify(tc.ctx, info, tc.apiErr, tc.panic))
			m := New()
			m.Observe("model", tc.ctx, info, tc.apiErr, tc.panic, 2*time.Second)
			assert.Equal(t, float64(1), testutil.ToFloat64(m.requests.WithLabelValues("model", tc.result)))
			registry := prometheus.NewPedanticRegistry()
			require.NoError(t, registry.Register(m))
			families, err := registry.Gather()
			require.NoError(t, err)
			for _, family := range families {
				if family.GetName() == "new_api_relay_request_duration_seconds" {
					h := family.Metric[0].Histogram
					assert.EqualValues(t, 1, h.GetSampleCount())
					assert.Equal(t, 2.0, h.GetSampleSum())
					bounds := []float64{}
					for _, b := range h.Bucket {
						bounds = append(bounds, b.GetUpperBound())
					}
					assert.Equal(t, []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1800}, bounds)
				}
			}
		})
	}
	assert.Equal(t, "unknown", Classify(context.Background(), nil, nil, false))
	assert.Equal(t, "unknown", Classify(context.Background(), &relaycommon.RelayInfo{IsStream: true}, nil, false))
}

func TestModelLabelsAndProcessLifetime(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"gpt-4o@temperature:0.12345", "gpt-4o"}, {"arbitrary-secret", "unknown"}, {"", "unknown"},
		{"gpt-4-gizmo-user-value", "gpt-4-gizmo-*"},
	} {
		assert.Equal(t, tc.want, ModelLabel(tc.input, []string{"gpt-4o", "gpt-4-gizmo-*"}))
	}
	m := New()
	m.Reject("model", 7)
	m.Observe("model", context.Background(), &relaycommon.RelayInfo{}, nil, false, time.Second)
	assert.Equal(t, 1.0, testutil.ToFloat64(m.rejected.WithLabelValues("model", "7")), "a later success must not erase rejection")
	restarted := New()
	assert.Equal(t, 0.0, testutil.ToFloat64(restarted.rejected.WithLabelValues("model", "7")))
}

// Same classification workload, with/without recording and with a scrape per
// 1000 requests. This measures instrumentation cost, not upstream latency.
func BenchmarkLogicalMetrics(b *testing.B) {
	for _, mode := range []string{"baseline", "record", "record_scrape"} {
		b.Run(mode, func(b *testing.B) {
			m := New()
			registry := prometheus.NewRegistry()
			registry.MustRegister(m)
			handler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
			request := httptest.NewRequest("GET", "/metrics", nil)
			info := &relaycommon.RelayInfo{}
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if mode == "baseline" {
					_ = Classify(ctx, info, nil, false)
				} else {
					m.Observe("gpt-4o", ctx, info, nil, false, time.Second)
				}
				if mode == "record_scrape" && i%1000 == 0 {
					recorder := httptest.NewRecorder()
					handler.ServeHTTP(recorder, request)
					if recorder.Code != 200 {
						b.Fatal(recorder.Code)
					}
				}
			}
		})
	}
}
