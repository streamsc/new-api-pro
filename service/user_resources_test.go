package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUserResourcesAtomicWindowsAndExpiry(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "redis"}[shared], func(t *testing.T) {
			now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
			advance := func(d time.Duration) { now = now.Add(d) }
			if shared {
				server, _ := useChannelConcurrencyRedis(t)
				server.SetTime(now)
				advance = func(d time.Duration) { now = now.Add(d); server.SetTime(now) }
			} else {
				useMemoryChannelConcurrency(t)
				userResourcesMemory.users = map[int]*memoryUserResources{}
			}
			ctx := context.Background()
			limits := setting.UserResourceLimits{Concurrency: setting.ResourceLimit{Enabled: true, Limit: 2}, InputTPM: setting.ResourceLimit{Enabled: true, Limit: 10}, RPM: setting.ResourceLimit{Enabled: true, Limit: 2}}
			u, r, err := userResources(ctx, 81, "one", 6, limits, now)
			require.NoError(t, err)
			require.Nil(t, r)
			assert.Equal(t, UserResourceUsage{1, 6, 1}, u)
			_, r, err = userResources(ctx, 81, "rejected", 5, limits, now)
			require.NoError(t, err)
			require.NotNil(t, r)
			assert.Equal(t, "user_input_tpm_exceeded", r.Code)
			assert.Equal(t, 60, r.RetryAfter)
			u, _, err = userResources(ctx, 81, "", 0, limits, now)
			require.NoError(t, err)
			assert.Equal(t, UserResourceUsage{1, 6, 1}, u)
			advance(30 * time.Second)
			u, r, err = userResources(ctx, 81, "two", 4, limits, now)
			require.NoError(t, err)
			require.Nil(t, r)
			assert.Equal(t, UserResourceUsage{2, 10, 2}, u)
			_, r, err = userResources(ctx, 81, "three", 0, limits, now)
			require.NoError(t, err)
			require.NotNil(t, r)
			assert.Equal(t, "user_concurrency_exceeded", r.Code)
			assert.Zero(t, r.RetryAfter)
			advance(30 * time.Second)
			u, _, err = userResources(ctx, 81, "", 0, limits, now)
			require.NoError(t, err)
			assert.Equal(t, UserResourceUsage{2, 4, 1}, u)
			advance(30 * time.Second)
			u, _, err = userResources(ctx, 81, "", 0, limits, now)
			require.NoError(t, err)
			assert.Equal(t, UserResourceUsage{2, 0, 0}, u)
			advance(29 * time.Minute)
			u, _, err = userResources(ctx, 81, "", 0, limits, now)
			require.NoError(t, err)
			assert.Equal(t, UserResourceUsage{}, u)
		})
	}
}

func TestUserResourcesReleaseAndUnlimitedTransition(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "redis"}[shared], func(t *testing.T) {
			if shared {
				useChannelConcurrencyRedis(t)
			} else {
				useMemoryChannelConcurrency(t)
				userResourcesMemory.users = map[int]*memoryUserResources{}
			}
			ctx, cancel := context.WithCancel(context.Background())
			release, rejection, err := AcquireUserResources(ctx, 82, 20, setting.UserResourceLimits{})
			require.NoError(t, err)
			require.Nil(t, rejection)
			cancel()
			release()
			release()
			usage, err := GetUserResourceUsage(context.Background(), 82)
			require.NoError(t, err)
			assert.Equal(t, UserResourceUsage{0, 20, 1}, usage)
			_, rejection, err = AcquireUserResources(context.Background(), 82, 0, setting.UserResourceLimits{RPM: setting.ResourceLimit{Enabled: true, Limit: 1}})
			require.NoError(t, err)
			require.NotNil(t, rejection)
			assert.Equal(t, "user_rpm_exceeded", rejection.Code)
			_, rejection, err = AcquireUserResources(context.Background(), 82, 0, setting.UserResourceLimits{Concurrency: setting.ResourceLimit{Enabled: true, Limit: 0}})
			require.NoError(t, err)
			require.NotNil(t, rejection)
			assert.Equal(t, "user_concurrency_exceeded", rejection.Code)
		})
	}
}

func TestUserResourcesSimultaneousAdmission(t *testing.T) {
	useChannelConcurrencyRedis(t)
	limits := setting.UserResourceLimits{Concurrency: setting.ResourceLimit{Enabled: true, Limit: 1}}
	var wg sync.WaitGroup
	releases := make(chan func(), 2)
	for range 2 {
		wg.Go(func() {
			release, rejection, err := AcquireUserResources(context.Background(), 83, 8, limits)
			assert.NoError(t, err)
			if rejection == nil {
				releases <- release
			}
		})
	}
	wg.Wait()
	close(releases)
	require.Len(t, releases, 1)
	usage, err := GetUserResourceUsage(context.Background(), 83)
	require.NoError(t, err)
	assert.Equal(t, UserResourceUsage{1, 8, 1}, usage)
	for release := range releases {
		release()
	}
}

func TestResourceStoreFailureDoesNotCancelInFlight(t *testing.T) {
	_, client := useChannelConcurrencyRedis(t)
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	lease, _, err := AcquireChannelConcurrency(parent, 84, 1, "live")
	require.NoError(t, err)
	release, r, err := AcquireUserResources(parent, 84, 3, setting.UserResourceLimits{})
	require.NoError(t, err)
	require.Nil(t, r)
	require.NoError(t, client.Close())
	_, _, err = AcquireUserResources(parent, 84, 3, setting.UserResourceLimits{RPM: setting.ResourceLimit{Enabled: true, Limit: 2}})
	require.Error(t, err)
	unlimited, r, err := AcquireUserResources(parent, 84, 3, setting.UserResourceLimits{})
	require.NoError(t, err)
	require.Nil(t, r)
	unlimited()
	channel, count, err := AcquireChannelConcurrency(parent, 84, 0, "unlimited")
	require.NoError(t, err)
	assert.Equal(t, -1, count)
	channel.Release()
	_, _, err = AcquireChannelConcurrency(parent, 84, 1, "limited")
	assert.ErrorIs(t, err, ErrChannelConcurrencyStore)
	_, err = GetUserResourceUsage(parent, 84)
	require.Error(t, err)
	release()
	assert.NoError(t, lease.Context().Err())
	lease.Release()
	assert.NoError(t, parent.Err())
}

func TestResourceRedisDatabasesAndExpiredChannelDoNotCancel(t *testing.T) {
	server, client := useChannelConcurrencyRedis(t)
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	lease, _, err := AcquireChannelConcurrency(parent, 85, 1, "request")
	require.NoError(t, err)
	defer lease.Release()
	release, r, err := AcquireUserResources(parent, 85, 3, setting.UserResourceLimits{})
	require.NoError(t, err)
	require.Nil(t, r)
	defer release()
	other := redis.NewClient(&redis.Options{Addr: server.Addr(), DB: 1})
	defer other.Close()
	common.RDB = other
	usage, err := GetUserResourceUsage(parent, 85)
	require.NoError(t, err)
	assert.Zero(t, usage.Concurrency)
	counts, err := GetChannelConcurrencyCounts(parent, []int{85})
	require.NoError(t, err)
	assert.Zero(t, counts[85])
	common.RDB = client
	server.SetTime(time.Now().Add(31 * time.Minute))
	counts, err = GetChannelConcurrencyCounts(parent, []int{85})
	require.NoError(t, err)
	assert.Zero(t, counts[85])
	assert.NoError(t, lease.Context().Err())
	// Lost Redis data is accepted: recovery uses existing state directly.
	require.NoError(t, client.Del(parent, userResourceKeys(85)...).Err())
	usage, err = GetUserResourceUsage(parent, 85)
	require.NoError(t, err)
	assert.Equal(t, UserResourceUsage{}, usage)
}

func TestUserResourceConfigurationSemantics(t *testing.T) {
	config, err := setting.ParseUserResourcesConfig(`{"defaults":{"rpm":{"enabled":true,"limit":5}},"users":{"7":{"rpm":{"enabled":false,"limit":0}},"8":{"rpm":{"enabled":true,"limit":0}}}}`)
	require.NoError(t, err)
	assert.True(t, config.ForUser(9).RPM.Enabled)
	assert.Equal(t, 5, config.ForUser(9).RPM.Limit)
	assert.False(t, config.ForUser(7).RPM.Enabled)
	assert.True(t, config.ForUser(8).RPM.Enabled)
	assert.Zero(t, config.ForUser(8).RPM.Limit)
	_, err = setting.ParseUserResourcesConfig(`{"defaults":{"rpm":{"limit":-1}}}`)
	require.Error(t, err)
	previous := common.RedisEnabled
	t.Cleanup(func() { common.RedisEnabled = previous })
	common.RedisEnabled = false
	t.Setenv("GATEWAY_REPLICA_MODE", "cluster")
	require.Error(t, InitResourceCounters())
}

func TestResourceInputEstimateIgnoresBillingAndOutput(t *testing.T) {
	previous := constant.CountToken
	constant.CountToken = false
	defer func() { constant.CountToken = previous }()
	meta := &types.TokenCountMeta{CombineText: "system instructions and user input", MessagesCount: 2, ToolsCount: 1, Files: []*types.FileMeta{{FileType: types.FileTypeImage, Detail: "high"}}}
	before := EstimateUserResourceInput(meta, "gpt-4o", types.RelayFormatOpenAI)
	require.Positive(t, before)
	meta.MaxTokens = 2147483647
	meta.ImagePriceRatio = 999
	meta.BillingRatios = map[string]float64{"cache": 0.01, "n": 10}
	assert.Equal(t, before, EstimateUserResourceInput(meta, "gpt-4o", types.RelayFormatOpenAI))
	meta.CombineText += " additional uncached input"
	assert.Greater(t, EstimateUserResourceInput(meta, "gpt-4o", types.RelayFormatOpenAI), before)
}

func TestResourceCountersRedisNeedsNoDeploymentID(t *testing.T) {
	useChannelConcurrencyRedis(t)
	t.Setenv("GATEWAY_DEPLOYMENT_ID", "")
	for _, mode := range []string{"", "single", "cluster"} {
		t.Setenv("GATEWAY_REPLICA_MODE", mode)
		require.NoError(t, InitResourceCounters())
	}
}
