package service

import (
	"context"
	"fmt"
	"github.com/QuantumNous/new-api/setting"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelConcurrencyRealRedis(t *testing.T) {
	address := os.Getenv("CONCURRENCY_TEST_REDIS_ADDR")
	if address == "" {
		t.Skip("dedicated Redis server not configured (test uses CLIENT PAUSE)")
	}
	client := redis.NewClient(&redis.Options{Addr: address, DB: 13})
	peer := redis.NewClient(&redis.Options{Addr: address, DB: 13})
	defer client.Close()
	defer peer.Close()
	previousEnabled, previousClient := common.RedisEnabled, common.RDB
	common.RedisEnabled, common.RDB = true, client
	defer func() { common.RedisEnabled, common.RDB = previousEnabled, previousClient }()
	ctx := context.Background()
	require.NoError(t, client.Ping(ctx).Err())
	lease, _, err := AcquireChannelConcurrency(ctx, 91501, 10, "issue5-real-redis")
	require.NoError(t, err)
	defer lease.Release()
	first, err := GetChannelConcurrencyCounts(ctx, []int{91501})
	require.NoError(t, err)
	common.RDB = peer
	second, err := GetChannelConcurrencyCounts(ctx, []int{91501})
	require.NoError(t, err)
	assert.Equal(t, map[int]int{91501: 1}, first)
	assert.Equal(t, first, second)
	require.NoError(t, client.Do(ctx, "CLIENT", "PAUSE", 5000, "ALL").Err())
	defer client.Do(ctx, "CLIENT", "UNPAUSE")
	readCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	_, err = GetChannelConcurrencyCounts(readCtx, []int{91501})
	assert.Error(t, err)
	assert.ErrorIs(t, readCtx.Err(), context.DeadlineExceeded)
	cancel()
	require.NoError(t, client.Do(ctx, "CLIENT", "UNPAUSE").Err())
	counts, err := GetChannelConcurrencyCounts(ctx, []int{91501})
	require.NoError(t, err)
	assert.Equal(t, map[int]int{91501: 1}, counts)
	assert.NoError(t, lease.Context().Err(), "scrape timeout must not cancel the request")
	lease.Release()
	counts, err = GetChannelConcurrencyCounts(ctx, []int{91501})
	require.NoError(t, err)
	assert.Zero(t, counts[91501])
}

func TestUserResourcesRealRedisProcesses(t *testing.T) {
	address := os.Getenv("CONCURRENCY_TEST_REDIS_ADDR")
	if address == "" {
		t.Skip("dedicated Redis not configured")
	}
	client := redis.NewClient(&redis.Options{Addr: address, DB: 13})
	defer client.Close()
	oldEnabled, oldClient := common.RedisEnabled, common.RDB
	common.RedisEnabled, common.RDB = true, client
	defer func() { common.RedisEnabled, common.RDB = oldEnabled, oldClient }()
	keys := userResourceKeys(9981)
	if os.Getenv("ISSUE8_CHILD") == "1" {
		_, rejected, err := AcquireUserResources(context.Background(), 9981, 7, setting.UserResourceLimits{Concurrency: setting.ResourceLimit{Enabled: true, Limit: 1}})
		require.NoError(t, err)
		if rejected == nil {
			fmt.Println("ADMITTED")
		} else {
			fmt.Println(rejected.Code)
		}
		// Simulate process death: the reservation deliberately remains in Redis.
		return
	}
	require.NoError(t, client.Del(context.Background(), keys...).Err())
	defer client.Del(context.Background(), keys...)
	for _, expected := range []string{"ADMITTED", "user_concurrency_exceeded"} {
		command := exec.Command(os.Args[0], "-test.run=^TestUserResourcesRealRedisProcesses$", "-test.count=1")
		command.Env = append(os.Environ(), "ISSUE8_CHILD=1")
		output, err := command.CombinedOutput()
		require.NoError(t, err, string(output))
		assert.Contains(t, string(output), expected)
	}
	usage, err := GetUserResourceUsage(context.Background(), 9981)
	require.NoError(t, err)
	assert.Equal(t, UserResourceUsage{1, 7, 1}, usage)
	// Independent deployments use separate Redis databases, not a key namespace.
	other := redis.NewClient(&redis.Options{Addr: address, DB: 14})
	defer other.Close()
	require.NoError(t, other.Del(context.Background(), keys...).Err())
	defer other.Del(context.Background(), keys...)
	common.RDB = other
	release, rejected, err := AcquireUserResources(context.Background(), 9981, 2, setting.UserResourceLimits{Concurrency: setting.ResourceLimit{Enabled: true, Limit: 1}})
	require.NoError(t, err)
	require.Nil(t, rejected)
	release()
	common.RDB = client
	usage, err = GetUserResourceUsage(context.Background(), 9981)
	require.NoError(t, err)
	assert.Equal(t, UserResourceUsage{1, 7, 1}, usage)
	slots, err := client.ZRangeWithScores(context.Background(), keys[0], 0, -1).Result()
	require.NoError(t, err)
	require.Len(t, slots, 1)
	now, err := client.Time(context.Background()).Result()
	require.NoError(t, err)
	assert.InDelta(t, 30*time.Minute.Seconds(), (slots[0].Score-float64(now.UnixMilli()))/1000, 5)
	// Accelerate only the server-side timestamp, without sleeping for 30 minutes.
	require.NoError(t, client.ZAdd(context.Background(), keys[0], &redis.Z{Member: slots[0].Member, Score: 0}).Err())
	usage, err = GetUserResourceUsage(context.Background(), 9981)
	require.NoError(t, err)
	assert.Equal(t, UserResourceUsage{0, 7, 1}, usage)
}
