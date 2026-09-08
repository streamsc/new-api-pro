package service

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/concurrencymetrics"
	"github.com/go-redis/redis/v8"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This test shuts down the elected master. Only use a disposable Sentinel cluster
// with one replica and a down-after-milliseconds interval of at least 5000.
func TestChannelConcurrencySentinelFailover(t *testing.T) {
	address := os.Getenv("CONCURRENCY_TEST_SENTINEL_ADDR")
	if address == "" {
		t.Skip("disposable Sentinel cluster not configured (test shuts down master)")
	}
	replicaAddress := os.Getenv("CONCURRENCY_TEST_SENTINEL_REPLICA_ADDR")
	require.NotEmpty(t, replicaAddress)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sentinel := redis.NewSentinelClient(&redis.Options{Addr: address})
	defer sentinel.Close()
	master, err := sentinel.GetMasterAddrByName(ctx, "issue5").Result()
	require.NoError(t, err)
	oldAddress := net.JoinHostPort(master[0], master[1])
	oldMaster := redis.NewClient(&redis.Options{Addr: oldAddress, DB: 13, MaxRetries: -1})
	defer oldMaster.Close()
	replica := redis.NewClient(&redis.Options{Addr: replicaAddress, DB: 13})
	defer replica.Close()
	previousEnabled, previousClient, previousFrequency := common.RedisEnabled, common.RDB, common.SyncFrequency
	defer func() {
		common.RedisEnabled, common.RDB, common.SyncFrequency = previousEnabled, previousClient, previousFrequency
	}()
	t.Setenv("REDIS_CONN_STRING", "redis://unused:6379/13")
	t.Setenv("REDIS_SENTINEL_MASTER_NAME", "issue5")
	t.Setenv("REDIS_SENTINEL_ADDRS", address)
	t.Setenv("REDIS_SENTINEL_USERNAME", "")
	t.Setenv("REDIS_SENTINEL_PASSWORD", "")
	common.RedisEnabled = true
	require.NoError(t, common.InitRedisClient())
	defer common.RDB.Close()
	peer := redis.NewFailoverClient(&redis.FailoverOptions{MasterName: "issue5", SentinelAddrs: []string{address}, DB: 13})
	defer peer.Close()
	const channelID = 91502
	const member = "issue5-sentinel-live-lease"
	lease, _, err := AcquireChannelConcurrency(ctx, channelID, 10, member)
	require.NoError(t, err)
	defer lease.Release()
	score, err := common.RDB.ZScore(ctx, channelConcurrencyKey(channelID), member).Result()
	require.NoError(t, err)
	// Observe the actual replicated lease before failing the master; asynchronous
	// replication cannot guarantee retention of an unreplicated write.
	require.Eventually(t, func() bool {
		got, readErr := replica.ZScore(ctx, channelConcurrencyKey(channelID), member).Result()
		return readErr == nil && got == score
	}, 10*time.Second, 100*time.Millisecond)
	setting := `{"max_concurrency":10}`
	readConfigs := func(context.Context) ([]model.ChannelConcurrencyConfig, error) {
		return []model.ChannelConcurrencyConfig{{ID: channelID, Setting: &setting}}, nil
	}
	before := collectChannelConcurrency(ctx, "redis", readConfigs, GetChannelConcurrencyCounts)
	require.True(t, before.ConcurrencySuccess)
	require.Equal(t, 1, *before.Channels[0].InFlight)
	peerCount, err := peer.ZCard(ctx, channelConcurrencyKey(channelID)).Result()
	require.NoError(t, err)
	require.EqualValues(t, 1, peerCount)
	t.Logf("before: master=%s; application count=1; independent Sentinel client count=1; lease replicated", oldAddress)

	started := time.Now()
	shutdownErr := oldMaster.ShutdownNoSave(ctx).Err()
	t.Logf("SHUTDOWN NOSAVE result: %v", shutdownErr)
	require.Error(t, oldMaster.Ping(ctx).Err(), "master must actually be unavailable")
	failure := collectChannelConcurrency(ctx, "redis", readConfigs, GetChannelConcurrencyCounts)
	require.True(t, failure.ConfigSuccess)
	require.False(t, failure.ConcurrencySuccess)
	require.Nil(t, failure.Channels[0].InFlight, "failed read must not produce a zero or stale count")
	registry := prometheus.NewRegistry()
	registry.MustRegister(concurrencymetrics.Collector{Snapshot: failure})
	require.NoError(t, testutil.GatherAndCompare(registry, strings.NewReader(`# HELP new_api_channel_concurrency_limit Persisted concurrency limit; absent for unlimited or invalid configuration.
# TYPE new_api_channel_concurrency_limit gauge
new_api_channel_concurrency_limit{channel_id="91502",scope="redis"} 10
# HELP new_api_channel_concurrency_limit_enabled Whether the persisted concurrency limit is enabled.
# TYPE new_api_channel_concurrency_limit_enabled gauge
new_api_channel_concurrency_limit_enabled{channel_id="91502",scope="redis"} 1
# HELP new_api_concurrency_scrape_success Whether this scrape fully collected the component.
# TYPE new_api_concurrency_scrape_success gauge
new_api_concurrency_scrape_success{component="channel_config",scope="redis"} 1
new_api_concurrency_scrape_success{component="concurrency",scope="redis"} 0
`)))
	t.Logf("failure at %s: complete metrics comparison passed; counts absent, config retained, concurrency success=0", time.Since(started))
	var newAddress string
	require.Eventually(t, func() bool {
		current, readErr := sentinel.GetMasterAddrByName(ctx, "issue5").Result()
		if readErr != nil {
			return false
		}
		newAddress = net.JoinHostPort(current[0], current[1])
		if newAddress == oldAddress {
			return false
		}
		counts, readErr := GetChannelConcurrencyCounts(ctx, []int{channelID})
		return readErr == nil && counts[channelID] == 1
	}, 25*time.Second, 200*time.Millisecond)
	after := collectChannelConcurrency(ctx, "redis", readConfigs, GetChannelConcurrencyCounts)
	require.True(t, after.ConcurrencySuccess)
	require.Equal(t, 1, *after.Channels[0].InFlight)
	assert.Equal(t, "redis", after.Scope)
	assert.False(t, lease.Lost())
	peerCount, err = peer.ZCard(ctx, channelConcurrencyKey(channelID)).Result()
	require.NoError(t, err)
	assert.EqualValues(t, 1, peerCount)
	t.Logf("recovered at %s: new master=%s; both clients count=1; lease remains valid", time.Since(started), newAddress)
	require.Eventually(t, func() bool {
		newScore, readErr := peer.ZScore(ctx, channelConcurrencyKey(channelID), member).Result()
		return readErr == nil && newScore > score
	}, 35*time.Second, 200*time.Millisecond)
	require.False(t, lease.Lost())
	t.Logf("lease renewal observed on promoted master at %s", time.Since(started))
	second, _, err := AcquireChannelConcurrency(ctx, channelID, 10, "issue5-after-failover")
	require.NoError(t, err)
	defer second.Release()
	counts, err := GetChannelConcurrencyCounts(ctx, []int{channelID})
	require.NoError(t, err)
	assert.Equal(t, 2, counts[channelID])
	second.Release()
	lease.Release()
	counts, err = GetChannelConcurrencyCounts(ctx, []int{channelID})
	require.NoError(t, err)
	assert.Zero(t, counts[channelID])
	t.Log("new acquisition count=2; both releases count=0")
}
