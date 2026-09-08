package concurrencymetrics

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

type ChannelSample struct {
	ChannelID int
	InFlight  *int
	Limit     *int
}

// Snapshot belongs to one scrape and must not be mutated during collection.
type Snapshot struct {
	Scope              string
	ConfigSuccess      bool
	ConcurrencySuccess bool
	Channels           []ChannelSample
}

var (
	inFlight      = prometheus.NewDesc("new_api_channel_concurrency_in_flight", "Current valid gateway concurrency reservations.", []string{"channel_id", "scope"}, nil)
	limit         = prometheus.NewDesc("new_api_channel_concurrency_limit", "Persisted concurrency limit; absent for unlimited or invalid configuration.", []string{"channel_id", "scope"}, nil)
	limitEnabled  = prometheus.NewDesc("new_api_channel_concurrency_limit_enabled", "Whether the persisted concurrency limit is enabled.", []string{"channel_id", "scope"}, nil)
	scrapeSuccess = prometheus.NewDesc("new_api_concurrency_scrape_success", "Whether this scrape fully collected the component.", []string{"component", "scope"}, nil)
)

type Collector struct {
	Snapshot Snapshot
}

func (c Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- inFlight
	ch <- limit
	ch <- limitEnabled
	ch <- scrapeSuccess
}

func (c Collector) Collect(ch chan<- prometheus.Metric) {
	for _, sample := range c.Snapshot.Channels {
		id := strconv.Itoa(sample.ChannelID)
		if sample.InFlight != nil {
			c.emit(ch, inFlight, float64(*sample.InFlight), id, c.Snapshot.Scope)
		}
		if sample.Limit != nil {
			enabled := 0.0
			if *sample.Limit > 0 {
				enabled = 1
				c.emit(ch, limit, float64(*sample.Limit), id, c.Snapshot.Scope)
			}
			c.emit(ch, limitEnabled, enabled, id, c.Snapshot.Scope)
		}
	}
	for component, success := range map[string]bool{"channel_config": c.Snapshot.ConfigSuccess, "concurrency": c.Snapshot.ConcurrencySuccess} {
		value := 0.0
		if success {
			value = 1
		}
		c.emit(ch, scrapeSuccess, value, component, c.Snapshot.Scope)
	}
}

func (c Collector) emit(ch chan<- prometheus.Metric, desc *prometheus.Desc, value float64, labels ...string) {
	metric, err := prometheus.NewConstMetric(desc, prometheus.GaugeValue, value, labels...)
	if err != nil {
		ch <- prometheus.NewInvalidMetric(desc, err)
		return
	}
	ch <- metric
}
