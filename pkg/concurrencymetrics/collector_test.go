package concurrencymetrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestCollectorExposesOnlyCurrentKnownValues(t *testing.T) {
	zero, two, ten := 0, 2, 10
	registry := prometheus.NewRegistry()
	require.NoError(t, registry.Register(Collector{Snapshot{Scope: "redis", ConfigSuccess: false, ConcurrencySuccess: true, Channels: []ChannelSample{
		{ChannelID: 1, InFlight: &zero, Limit: &ten}, {ChannelID: 2, InFlight: &two, Limit: &zero}, {ChannelID: 3, InFlight: &two},
	}}}))
	require.NoError(t, testutil.GatherAndCompare(registry, strings.NewReader(`# HELP new_api_channel_concurrency_in_flight Current valid gateway concurrency reservations.
# TYPE new_api_channel_concurrency_in_flight gauge
new_api_channel_concurrency_in_flight{channel_id="1",scope="redis"} 0
new_api_channel_concurrency_in_flight{channel_id="2",scope="redis"} 2
new_api_channel_concurrency_in_flight{channel_id="3",scope="redis"} 2
# HELP new_api_channel_concurrency_limit Persisted concurrency limit; absent for unlimited or invalid configuration.
# TYPE new_api_channel_concurrency_limit gauge
new_api_channel_concurrency_limit{channel_id="1",scope="redis"} 10
# HELP new_api_channel_concurrency_limit_enabled Whether the persisted concurrency limit is enabled.
# TYPE new_api_channel_concurrency_limit_enabled gauge
new_api_channel_concurrency_limit_enabled{channel_id="1",scope="redis"} 1
new_api_channel_concurrency_limit_enabled{channel_id="2",scope="redis"} 0
# HELP new_api_concurrency_scrape_success Whether this scrape fully collected the component.
# TYPE new_api_concurrency_scrape_success gauge
new_api_concurrency_scrape_success{component="channel_config",scope="redis"} 0
new_api_concurrency_scrape_success{component="concurrency",scope="redis"} 1
`)))
	next := prometheus.NewRegistry()
	require.NoError(t, next.Register(Collector{Snapshot{Scope: "redis"}}))
	require.NoError(t, testutil.GatherAndCompare(next, strings.NewReader(`# HELP new_api_concurrency_scrape_success Whether this scrape fully collected the component.
# TYPE new_api_concurrency_scrape_success gauge
new_api_concurrency_scrape_success{component="channel_config",scope="redis"} 0
new_api_concurrency_scrape_success{component="concurrency",scope="redis"} 0
`)))
}
