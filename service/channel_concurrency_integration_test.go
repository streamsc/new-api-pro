package service

import (
	"context"
	"os"
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
	assert.False(t, lease.Lost(), "scrape timeout must preserve a valid reservation")
	lease.Release()
	counts, err = GetChannelConcurrencyCounts(ctx, []int{91501})
	require.NoError(t, err)
	assert.Zero(t, counts[91501])
}
