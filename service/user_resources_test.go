package service

import (
	"context"
	"fmt"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
	"os"
	"slices"
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
			u, r, err := userResources(ctx, 81, "one", 6, UserResourceAdmission{Total: limits}, now)
			require.NoError(t, err)
			require.Nil(t, r)
			assert.Equal(t, UserResourceUsage{1, 6, 1}, u[0])
			_, r, err = userResources(ctx, 81, "rejected", 5, UserResourceAdmission{Total: limits}, now)
			require.NoError(t, err)
			require.NotNil(t, r)
			assert.Equal(t, "user_input_tpm_exceeded", r.Code)
			assert.Equal(t, 60, r.RetryAfter)
			u, _, err = userResources(ctx, 81, "", 0, UserResourceAdmission{Total: limits}, now)
			require.NoError(t, err)
			assert.Equal(t, UserResourceUsage{1, 6, 1}, u[0])
			advance(30 * time.Second)
			u, r, err = userResources(ctx, 81, "two", 4, UserResourceAdmission{Total: limits}, now)
			require.NoError(t, err)
			require.Nil(t, r)
			assert.Equal(t, UserResourceUsage{2, 10, 2}, u[0])
			_, r, err = userResources(ctx, 81, "three", 0, UserResourceAdmission{Total: limits}, now)
			require.NoError(t, err)
			require.NotNil(t, r)
			assert.Equal(t, "user_concurrency_exceeded", r.Code)
			assert.Zero(t, r.RetryAfter)
			advance(30 * time.Second)
			u, _, err = userResources(ctx, 81, "", 0, UserResourceAdmission{Total: limits}, now)
			require.NoError(t, err)
			assert.Equal(t, UserResourceUsage{2, 4, 1}, u[0])
			advance(30 * time.Second)
			u, _, err = userResources(ctx, 81, "", 0, UserResourceAdmission{Total: limits}, now)
			require.NoError(t, err)
			assert.Equal(t, UserResourceUsage{2, 0, 0}, u[0])
			advance(29 * time.Minute)
			u, _, err = userResources(ctx, 81, "", 0, UserResourceAdmission{Total: limits}, now)
			require.NoError(t, err)
			assert.Equal(t, UserResourceUsage{}, u[0])
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
			release, rejection, err := AcquireUserResources(ctx, 82, 20, UserResourceAdmission{Total: setting.UserResourceLimits{}})
			require.NoError(t, err)
			require.Nil(t, rejection)
			cancel()
			release()
			release()
			usage, err := GetUserResourceUsage(context.Background(), 82)
			require.NoError(t, err)
			assert.Equal(t, UserResourceUsage{0, 20, 1}, usage)
			_, rejection, err = AcquireUserResources(context.Background(), 82, 0, UserResourceAdmission{Total: setting.UserResourceLimits{RPM: setting.ResourceLimit{Enabled: true, Limit: 1}}})
			require.NoError(t, err)
			require.NotNil(t, rejection)
			assert.Equal(t, "user_rpm_exceeded", rejection.Code)
			_, rejection, err = AcquireUserResources(context.Background(), 82, 0, UserResourceAdmission{Total: setting.UserResourceLimits{Concurrency: setting.ResourceLimit{Enabled: true, Limit: 0}}})
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
			release, rejection, err := AcquireUserResources(context.Background(), 83, 8, UserResourceAdmission{Total: limits})
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
	release, r, err := AcquireUserResources(parent, 84, 3, UserResourceAdmission{Total: setting.UserResourceLimits{}})
	require.NoError(t, err)
	require.Nil(t, r)
	require.NoError(t, client.Close())
	_, _, err = AcquireUserResources(parent, 84, 3, UserResourceAdmission{Total: setting.UserResourceLimits{RPM: setting.ResourceLimit{Enabled: true, Limit: 2}}})
	require.Error(t, err)
	unlimited, r, err := AcquireUserResources(parent, 84, 3, UserResourceAdmission{Total: setting.UserResourceLimits{}})
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
	release, r, err := AcquireUserResources(parent, 85, 3, UserResourceAdmission{Total: setting.UserResourceLimits{}})
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

func TestUserModelJointAdmission(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "redis"}[shared], func(t *testing.T) {
			now := time.Now()
			if shared {
				server, _ := useChannelConcurrencyRedis(t)
				server.SetTime(now)
			} else {
				useMemoryChannelConcurrency(t)
			}
			userResourcesMemory.users = map[int]*memoryUserResources{}
			userResourcesMemory.models = map[userModelResourceKey]*memoryUserResources{}
			ctx := context.Background()
			config, err := setting.ParseUserResourcesConfig(`{"defaults":{"concurrency":{"enabled":true,"limit":3},"rpm":{"enabled":true,"limit":3},"input_tpm":{"enabled":true,"limit":10}},"models":{"A":{"defaults":{"concurrency":{"enabled":true,"limit":1},"rpm":{"enabled":true,"limit":2}},"users":{"42":{"concurrency":{"enabled":true,"limit":2}}}},"B":{"users":{"99":{"rpm":{"enabled":true,"limit":1}}}}}}`)
			require.NoError(t, err)
			admission := UserResourceAdmission{Total: config.ForUser(42), ModelName: "A", Model: config.ForModel(42, "A")}
			_, r, err := userResources(ctx, 42, "first", 4, admission, now)
			require.NoError(t, err)
			require.Nil(t, r)
			// Driver replay is idempotent for concurrency, requests and tokens together.
			u, r, err := userResources(ctx, 42, "first", 4, admission, now)
			require.NoError(t, err)
			require.Nil(t, r)
			require.Equal(t, []UserResourceUsage{{1, 4, 1}, {1, 4, 1}}, u)
			_, r, err = userResources(ctx, 42, "second", 3, admission, now)
			require.NoError(t, err)
			require.Nil(t, r)
			_, r, err = userResources(ctx, 42, "blocked", 1, admission, now)
			require.NoError(t, err)
			require.Equal(t, "user_model_concurrency_exceeded", r.Code)
			u, _, err = userResources(ctx, 42, "", 0, admission, now)
			require.NoError(t, err)
			require.Equal(t, []UserResourceUsage{{2, 7, 2}, {2, 7, 2}}, u)
			// Different exact names get independent model buckets, but still share totals.
			other := UserResourceAdmission{Total: config.ForUser(42), ModelName: "B", Model: config.ForModel(42, "B")}
			_, r, err = userResources(ctx, 42, "third", 2, other, now)
			require.NoError(t, err)
			require.Nil(t, r)
			_, r, err = userResources(ctx, 42, "total-blocked", 1, other, now)
			require.NoError(t, err)
			require.Equal(t, "user_concurrency_exceeded", r.Code)
			u, _, err = userResources(ctx, 42, "", 0, other, now)
			require.NoError(t, err)
			require.Equal(t, UserResourceUsage{1, 2, 1}, u[1])
			// A personal-only rule enrolls every actual user, without limiting others.
			require.NotNil(t, config.ForModel(42, "B"))
			require.False(t, config.ForModel(42, "B").Restricted())
			require.Nil(t, config.ForModel(42, "a"))
			independent := UserResourceAdmission{Total: config.ForUser(43), ModelName: "A", Model: config.ForModel(43, "A")}
			_, r, err = userResources(ctx, 43, "independent", 1, independent, now)
			require.NoError(t, err)
			require.Nil(t, r)
			rows, _, complete := GetUserModelResourcePage(ctx, config, 42, "B", 1, 20)
			require.True(t, complete)
			require.Len(t, rows, 1)
			require.Equal(t, 1, rows[0].Usage.RPM)
		})
	}
}

func TestUserModelZeroWindowHistoryAndRetryAfter(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "redis"}[shared], func(t *testing.T) {
			now := time.Now()
			advance := func(d time.Duration) { now = now.Add(d) }
			if shared {
				server, _ := useChannelConcurrencyRedis(t)
				server.SetTime(now)
				advance = func(d time.Duration) { now = now.Add(d); server.SetTime(now) }
			} else {
				useMemoryChannelConcurrency(t)
			}
			userResourcesMemory.users = map[int]*memoryUserResources{}
			userResourcesMemory.models = map[userModelResourceKey]*memoryUserResources{}
			ctx := context.Background()
			modelLimits := setting.UserResourceLimits{}
			admission := UserResourceAdmission{ModelName: "history", Model: &modelLimits}
			_, r, err := userResources(ctx, 701, "before-enable", 6, admission, now)
			require.NoError(t, err)
			require.Nil(t, r)
			advance(20 * time.Second)
			admission.Total.RPM = setting.ResourceLimit{Enabled: true, Limit: 1}
			modelLimits.InputTPM = setting.ResourceLimit{Enabled: true, Limit: 6}
			_, r, err = userResources(ctx, 701, "rejected", 1, admission, now)
			require.NoError(t, err)
			require.Equal(t, "user_rpm_exceeded", r.Code)
			require.Equal(t, 40, r.RetryAfter)
			advance(40 * time.Second)
			_, r, err = userResources(ctx, 701, "at-boundary", 1, admission, now)
			require.NoError(t, err)
			require.Nil(t, r)
			// Enabled zero denies even a zero-input request, in either layer.
			for _, modelLayer := range []bool{false, true} {
				zero := setting.UserResourceLimits{InputTPM: setting.ResourceLimit{Enabled: true, Limit: 0}}
				a := UserResourceAdmission{}
				if modelLayer {
					a.ModelName = "zero"
					a.Model = &zero
				} else {
					a.Total = zero
				}
				_, rejection, e := userResources(ctx, 702, "zero", 0, a, now)
				require.NoError(t, e)
				require.NotNil(t, rejection)
				require.Zero(t, rejection.RetryAfter)
			}
			// Concurrency cannot promise a reset time, even with a window blocker.
			admission.Total.Concurrency = setting.ResourceLimit{Enabled: true, Limit: 1}
			_, r, err = userResources(ctx, 701, "no-countdown", 1, admission, now)
			require.NoError(t, err)
			require.Zero(t, r.RetryAfter)
		})
	}
}

func TestUserModelRuleRemovalReleaseAndStoreFailure(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "redis"}[shared], func(t *testing.T) {
			if shared {
				useChannelConcurrencyRedis(t)
			} else {
				useMemoryChannelConcurrency(t)
			}
			ctx := context.Background()
			rule := setting.UserResourceLimits{Concurrency: setting.ResourceLimit{Enabled: true, Limit: 1}}
			a := UserResourceAdmission{ModelName: "removed", Model: &rule}
			release, r, err := AcquireUserResources(ctx, 710, 3, a)
			require.NoError(t, err)
			require.Nil(t, r)
			a.Model = nil // Release must keep its original bucket, independently of new rules.
			release()
			release()
			rows, _, complete := GetUserModelResourcePage(ctx, setting.UserResourcesConfig{}, 710, "removed", 1, 20)
			require.True(t, complete)
			require.Len(t, rows, 1)
			require.False(t, rows[0].Configured)
			require.Zero(t, rows[0].Usage.Concurrency)
			require.Equal(t, 1, rows[0].Usage.RPM)
			// Rebuilding the same rule reuses the remaining rolling-window history.
			rule.RPM = setting.ResourceLimit{Enabled: true, Limit: 1}
			a.Model = &rule
			_, r, err = AcquireUserResources(ctx, 710, 0, a)
			require.NoError(t, err)
			require.Equal(t, "user_model_rpm_exceeded", r.Code)
			if shared {
				require.NoError(t, common.RDB.Close())
				_, _, err = AcquireUserResources(ctx, 711, 0, a)
				require.Error(t, err)
				a.Model = &setting.UserResourceLimits{}
				admitted, r, e := AcquireUserResources(ctx, 711, 0, a)
				require.NoError(t, e)
				require.Nil(t, r)
				admitted()
				rows, _, complete = GetUserModelResourcePage(ctx, setting.UserResourcesConfig{}, 710, "removed", 1, 20)
				require.False(t, complete)
				require.Len(t, rows, 1)
				require.False(t, rows[0].Available)
				require.Nil(t, rows[0].Usage)
			}
		})
	}
}

func TestUserModelConfigInheritanceAndCache(t *testing.T) {
	raw := `{"defaults":{"rpm":{"enabled":true,"limit":100}},"users":{"42":{"rpm":{"enabled":true,"limit":20}}},"models":{"A":{"defaults":{"concurrency":{"enabled":true,"limit":2},"rpm":{"enabled":true,"limit":5}},"users":{"42":{"rpm":{"enabled":false,"limit":0}}}}}}`
	c, err := setting.ParseUserResourcesConfig(raw)
	require.NoError(t, err)
	require.Equal(t, 20, c.ForUser(42).RPM.Limit)
	require.False(t, c.ForModel(42, "A").RPM.Enabled)
	require.Equal(t, 2, c.ForModel(42, "A").Concurrency.Limit)
	rule := c.Models["A"]
	delete(rule.Users, "42")
	c.Models["A"] = rule
	require.Equal(t, 5, c.ForModel(42, "A").RPM.Limit)
	for _, name := range []string{"", " A", "A\n"} {
		require.Error(t, setting.ValidateResourceModel(name))
	}
	old := common.OptionMap
	common.OptionMap = map[string]string{setting.UserResourcesOption: raw}
	t.Cleanup(func() { common.OptionMap = old })
	first, err := setting.CurrentUserResourcesConfig()
	require.NoError(t, err)
	require.Equal(t, 20, first.ForUser(42).RPM.Limit)
	common.OptionMap[setting.UserResourcesOption] = `{"defaults":{"rpm":{"enabled":true,"limit":2}}}`
	next, err := setting.CurrentUserResourcesConfig()
	require.NoError(t, err)
	require.Equal(t, 2, next.ForUser(42).RPM.Limit)
	require.Equal(t, 20, first.ForUser(42).RPM.Limit, "published snapshots must stay immutable")
}

func TestUserModelCountersExpireAfterIdle(t *testing.T) {
	now := time.Now()
	key := userModelResourceKey{719, "counter-expiry"}
	recordUserModelResourceEvent(key, "", now.Add(-25*time.Hour))
	recordUserModelResourceEvent(key, "user_model_rpm_exceeded", now)
	userModelResourceEvents.Lock()
	defer userModelResourceEvents.Unlock()
	e := userModelResourceEvents.events[key]
	require.Equal(t, uint64(0), e.Admitted)
	require.Equal(t, uint64(1), e.Rejected["user_model_rpm_exceeded"])
	delete(userModelResourceEvents.events, key)
}

func TestUserModelMetricsSparseAndFaults(t *testing.T) {
	server, client := useChannelConcurrencyRedis(t)
	_ = server
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	connection, err := db.DB()
	require.NoError(t, err)
	connection.SetMaxOpenConns(1)
	defer connection.Close()
	oldDB, oldOptions := model.DB, common.OptionMap
	model.DB = db
	defer func() { model.DB = oldDB; common.OptionMap = oldOptions }()
	type row struct {
		Id        int
		DeletedAt gorm.DeletedAt
	}
	require.NoError(t, db.Table("users").AutoMigrate(&row{}))
	require.NoError(t, db.Table("users").Create(&[]row{{Id: 720}, {Id: 721}, {Id: 722}}).Error)
	common.OptionMap = map[string]string{setting.UserResourcesOption: `{"models":{"A":{"defaults":{"rpm":{"enabled":true,"limit":1}},"users":{"722":{"concurrency":{"enabled":true,"limit":2}}}}}}`}
	config, err := setting.CurrentUserResourcesConfig()
	require.NoError(t, err)
	a := UserResourceAdmission{ModelName: "A", Model: config.ForModel(720, "A")}
	release, r, err := AcquireUserResources(context.Background(), 720, 4, a)
	require.NoError(t, err)
	require.Nil(t, r)
	release()
	_, r, err = AcquireUserResources(context.Background(), 720, 4, a)
	require.NoError(t, err)
	require.Equal(t, "user_model_rpm_exceeded", r.Code)
	// Unconfigured request strings never create model gauges or model counters.
	plain, r, err := AcquireUserResources(context.Background(), 721, 2, UserResourceAdmission{ModelName: "arbitrary"})
	require.NoError(t, err)
	require.Nil(t, r)
	plain()
	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(UserResourcesCollector{Context: context.Background()})
	families, err := registry.Gather()
	require.NoError(t, err)
	found := map[string]map[string]float64{}
	for _, family := range families {
		if family.GetName() == "new_api_user_model_window_requests" {
			found[family.GetName()] = map[string]float64{}
			for _, metric := range family.Metric {
				for _, label := range metric.Label {
					if label.GetName() == "user_id" {
						found[family.GetName()][label.GetValue()] = metric.Gauge.GetValue()
					}
				}
			}
		}
	}
	require.Equal(t, map[string]float64{"720": 1, "722": 0}, found["new_api_user_model_window_requests"])
	require.NoError(t, client.Close())
	families, err = registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		require.NotEqual(t, "new_api_user_model_window_requests", family.GetName(), "unknown usage must be omitted")
		if family.GetName() == "new_api_user_model_usage_available" {
			for _, metric := range family.Metric {
				require.Zero(t, metric.Gauge.GetValue())
			}
		}
	}
}

func TestUserModelRealRedisAtomicityAndScale(t *testing.T) {
	address := os.Getenv("ISSUE20_REDIS_ADDR")
	if address == "" {
		t.Skip("dedicated real Redis not configured")
	}
	// The environment variable must point to an isolated test Redis. DB 15 is
	// reset before and after this fixture, so repeated runs cannot reuse history.
	client := redis.NewClient(&redis.Options{Addr: address, DB: 15})
	require.NoError(t, client.FlushDB(context.Background()).Err())
	defer func() {
		require.NoError(t, client.FlushDB(context.Background()).Err())
		require.NoError(t, client.Close())
	}()
	previous, enabled := common.RDB, common.RedisEnabled
	common.RDB, common.RedisEnabled = client, true
	defer func() { common.RDB, common.RedisEnabled = previous, enabled }()
	ctx := context.Background()
	rule := setting.UserResourceLimits{Concurrency: setting.ResourceLimit{Enabled: true, Limit: 3}}
	a := UserResourceAdmission{Total: setting.UserResourceLimits{Concurrency: setting.ResourceLimit{Enabled: true, Limit: 5}}, ModelName: "real-atomic", Model: &rule}
	var wg sync.WaitGroup
	releases := make(chan func(), 64)
	for range 64 {
		wg.Go(func() {
			release, rejection, err := AcquireUserResources(ctx, 730, 7, a)
			require.NoError(t, err)
			if rejection == nil {
				releases <- release
			}
		})
	}
	wg.Wait()
	close(releases)
	require.Len(t, releases, 3)
	usage, err := readResourceUsageBatch(ctx, []userModelResourceKey{{730, ""}, {730, "real-atomic"}})
	require.NoError(t, err)
	require.Equal(t, UserResourceUsage{3, 21, 3}, usage[userModelResourceKey{730, ""}])
	require.Equal(t, usage[userModelResourceKey{730, ""}], usage[userModelResourceKey{730, "real-atomic"}])
	for release := range releases {
		release()
	}
	// Lost-reply replay uses the same captured member; it never adds input twice.
	_, r, err := userResources(ctx, 731, "same-command", 9, a, time.Now())
	require.NoError(t, err)
	require.Nil(t, r)
	u, r, err := userResources(ctx, 731, "same-command", 9, a, time.Now())
	require.NoError(t, err)
	require.Nil(t, r)
	require.Equal(t, []UserResourceUsage{{1, 9, 1}, {1, 9, 1}}, u)
	latencies := make(chan time.Duration, 1000)
	var admissions sync.WaitGroup
	admissions.Go(func() {
		for range 1000 {
			start := time.Now()
			release, rejection, err := AcquireUserResources(ctx, 732, 1, UserResourceAdmission{ModelName: "concurrent", Model: &setting.UserResourceLimits{}})
			if !assert.NoError(t, err) || !assert.Nil(t, rejection) {
				return
			}
			release()
			latencies <- time.Since(start)
		}
	})
	start := time.Now()
	pipe := client.Pipeline()
	now := time.Now().Add(30 * time.Minute).UnixMilli()
	for id := 80000; id < 90001; id++ {
		pipe.ZAdd(ctx, userModelActiveKey, &redis.Z{Score: float64(now), Member: userModelMember(userModelResourceKey{id, "scale"})})
	}
	_, err = pipe.Exec(ctx)
	require.NoError(t, err)
	keys, complete := discoverUserModels(ctx, setting.UserResourcesConfig{}, 0)
	require.False(t, complete, "bounded discovery must report truncation")
	require.Len(t, keys, resourceDiscoveryLimit)
	for offset := 0; offset < len(keys); offset += resourceBatchSize {
		_, err := readResourceUsageBatch(ctx, keys[offset:min(offset+resourceBatchSize, len(keys))])
		require.NoError(t, err)
	}
	elapsed := time.Since(start)
	// Exercise the actual collector with the same bounded directory, indexed DB
	// lookups and metric serialization while admissions keep using Redis.
	db, dbErr := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, dbErr)
	sqlDB, dbErr := db.DB()
	require.NoError(t, dbErr)
	sqlDB.SetMaxOpenConns(1)
	defer sqlDB.Close()
	previousDB := model.DB
	model.DB = db
	defer func() { model.DB = previousDB }()
	type directoryUser struct {
		Id        int
		DeletedAt gorm.DeletedAt
	}
	require.NoError(t, db.Table("users").AutoMigrate(&directoryUser{}))
	users := make([]directoryUser, 0, 10004)
	for _, id := range []int{730, 731, 732} {
		users = append(users, directoryUser{Id: id})
	}
	for id := 80000; id < 90001; id++ {
		users = append(users, directoryUser{Id: id})
	}
	require.NoError(t, db.Table("users").CreateInBatches(users, 500).Error)
	scrapeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(UserResourcesCollector{Context: scrapeCtx})
	collectStarted := time.Now()
	families, gatherErr := registry.Gather()
	require.NoError(t, gatherErr)
	require.NoError(t, scrapeCtx.Err(), "the bounded collection must finish inside the monitoring budget")
	count := 0
	for _, family := range families {
		if family.GetName() == "new_api_user_model_window_requests" {
			count = len(family.Metric)
		}
		if family.GetName() == "new_api_user_resource_scrape_success" {
			for _, metric := range family.Metric {
				for _, label := range metric.Label {
					if label.GetName() == "component" && label.GetValue() == "usage" {
						require.Equal(t, 1., metric.Gauge.GetValue())
					}
				}
			}
		}
	}
	require.Greater(t, count, 9000, "active indexed combinations must be observable without expanding every model/user pair")
	t.Logf("actual Prometheus collector emitted %d model rows in %s", count, time.Since(collectStarted))
	admissions.Wait()
	close(latencies)
	samples := make([]time.Duration, 0, 1000)
	for latency := range latencies {
		samples = append(samples, latency)
	}
	slices.Sort(samples)
	require.Len(t, samples, 1000)
	t.Logf("real Redis: 64 contenders, 3 joint admissions; capped discovery and 100 batches for %d combinations in %s; concurrent admission+release p50=%s p95=%s max=%s", len(keys), elapsed, samples[500], samples[950], samples[999])
}

func BenchmarkUserResourceAdmission(b *testing.B) {
	for _, mode := range []string{"total", "model"} {
		b.Run(mode, func(b *testing.B) {
			previous := common.RedisEnabled
			common.RedisEnabled = false
			defer func() { common.RedisEnabled = previous }()
			userResourcesMemory.users = map[int]*memoryUserResources{}
			userResourcesMemory.models = map[userModelResourceKey]*memoryUserResources{}
			a := UserResourceAdmission{}
			if mode == "model" {
				a.ModelName = "bench"
				a.Model = &setting.UserResourceLimits{}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := range b.N {
				_, r, err := userResources(context.Background(), 740, fmt.Sprint(i), 1, a, time.Now())
				if err != nil || r != nil {
					b.Fatal(err, r)
				}
			}
		})
	}
}

func TestUserModelCounterCollectionReportsCardinalityLimit(t *testing.T) {
	userModelResourceEvents.Lock()
	previous := userModelResourceEvents.events
	userModelResourceEvents.events = map[userModelResourceKey]*userModelEvents{}
	for id := 1; id <= resourceDiscoveryLimit+1; id++ {
		userModelResourceEvents.events[userModelResourceKey{id, "cap"}] = &userModelEvents{LastSeen: time.Now(), Admitted: 1}
	}
	userModelResourceEvents.Unlock()
	defer func() {
		userModelResourceEvents.Lock()
		userModelResourceEvents.events = previous
		userModelResourceEvents.Unlock()
	}()
	metrics := make(chan prometheus.Metric, 1)
	require.False(t, collectUserModelCounters(context.Background(), metrics))
	require.Empty(t, metrics, "over-budget collection must not publish an arbitrary subset as complete")
}

func TestUserModelCountersDoNotPublishAfterScrapeDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	metrics := make(chan prometheus.Metric, 1)
	require.False(t, collectUserModelCounters(ctx, metrics))
	require.Empty(t, metrics)
}
