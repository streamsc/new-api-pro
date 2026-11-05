// Package relaymetrics records process-local logical HTTP relay metrics. It does
// not participate in routing, retries, accounting or cancellation decisions.
package relaymetrics

import (
	"context"
	"errors"
	"strconv"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/prometheus/client_golang/prometheus"
)

const ContextModelKey = "prometheus_relay_model"
const UnknownModel = "unknown"

// ModelLabel admits only normalized names from the selected channel's configured
// model list. In particular, a pinned channel does not make arbitrary input safe.
func ModelLabel(requested string, configured []string) string {
	name := ratio_setting.RoutingMatchModelName(requested)
	if name != "" {
		for _, candidate := range configured {
			if name == ratio_setting.RoutingMatchModelName(candidate) {
				return name
			}
		}
	}
	return UnknownModel
}

type Metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	rejected *prometheus.CounterVec
}

var Default = New()

func New() *Metrics {
	return &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "new_api_relay_requests_total", Help: "Finished logical HTTP relay requests by gateway outcome."}, []string{"model", "result"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "new_api_relay_request_duration_seconds", Help: "Logical HTTP relay duration including internal retries in seconds.", Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1800}}, []string{"model", "result"}),
		rejected: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "new_api_channel_concurrency_rejected_total", Help: "Channel slot acquisitions rejected by the channel concurrency limit."}, []string{"model", "channel_id"}),
	}
}

func (m *Metrics) Describe(ch chan<- *prometheus.Desc) {
	m.requests.Describe(ch)
	m.duration.Describe(ch)
	m.rejected.Describe(ch)
}
func (m *Metrics) Collect(ch chan<- prometheus.Metric) {
	m.requests.Collect(ch)
	m.duration.Collect(ch)
	m.rejected.Collect(ch)
}

// Observe is called once by the outer Relay defer, including early exits. ctx is
// the original incoming context, never the internally cancelled channel lease.
func (m *Metrics) Observe(model string, ctx context.Context, info *relaycommon.RelayInfo, apiErr *types.NewAPIError, panicked bool, elapsed time.Duration) {
	if model == "" {
		model = UnknownModel
	}
	result := Classify(ctx, info, apiErr, panicked)
	m.requests.WithLabelValues(model, result).Inc()
	m.duration.WithLabelValues(model, result).Observe(elapsed.Seconds())
}

// Reject is called only after an acquisition returns ErrChannelConcurrencyLimit.
func (m *Metrics) Reject(model string, channelID int) {
	if model == "" {
		model = UnknownModel
	}
	m.rejected.WithLabelValues(model, strconv.Itoa(channelID)).Inc()
}

// Classify uses existing facts; it deliberately does not reuse health-sample
// "ignored", or parse protocol events. Upstream cancellation alone is not proof
// of client cancellation. A normal completion wins over a late client cancel.
func Classify(ctx context.Context, info *relaycommon.RelayInfo, apiErr *types.NewAPIError, panicked bool) string {
	if panicked {
		return "failure"
	}
	var stream relaycommon.StreamOutcome
	if info != nil {
		stream = info.StreamStatus.OutcomeSnapshot()
	}
	completed := info != nil && apiErr == nil && !info.PerformanceBusinessRejection &&
		((!info.IsStream && info.StreamStatus == nil) ||
			(info.StreamStatus != nil && !stream.HasErrors && info.StreamStatus.EndError == nil &&
				(stream.Response == relaycommon.ResponseOutcomeCompleted ||
					(stream.Response == relaycommon.ResponseOutcomeUnknown && stream.EndReason == relaycommon.StreamEndReasonDone)) &&
				(stream.EndReason == relaycommon.StreamEndReasonEOF || stream.EndReason == relaycommon.StreamEndReasonDone)))
	if completed {
		return "success"
	}
	if stream.Response == relaycommon.ResponseOutcomeFailed || (info != nil && info.PerformanceBusinessRejection) {
		return "failure"
	}
	if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
		return "cancelled"
	}
	if apiErr != nil {
		return "failure"
	}
	if ctx != nil && ctx.Err() != nil {
		return "failure"
	}
	if info == nil {
		return "unknown"
	}
	if stream.HasErrors {
		return "failure"
	}
	if info.StreamStatus != nil && info.StreamStatus.EndError != nil {
		return "failure"
	}
	switch stream.EndReason {
	case relaycommon.StreamEndReasonTimeout, relaycommon.StreamEndReasonScannerErr, relaycommon.StreamEndReasonPanic:
		return "failure"
	}
	if stream.Response == relaycommon.ResponseOutcomeIncomplete {
		switch stream.IncompleteReason {
		case "max_output_tokens", "max_tokens":
			return "success"
		case "content_filter", "safety", "content_policy_violation":
			return "failure"
		default:
			return "unknown"
		}
	}
	if stream.ExpectsTerminal && stream.Response == relaycommon.ResponseOutcomeUnknown && stream.EndReason == relaycommon.StreamEndReasonEOF {
		return "failure"
	}
	return "unknown"
}
