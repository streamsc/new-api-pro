package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func useMemoryChannelConcurrency(t *testing.T) {
	t.Helper()
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	memoryChannelConcurrency.Lock()
	memoryChannelConcurrency.counts = make(map[int]int)
	memoryChannelConcurrency.Unlock()
	t.Cleanup(func() {
		memoryChannelConcurrency.Lock()
		memoryChannelConcurrency.counts = make(map[int]int)
		memoryChannelConcurrency.Unlock()
		common.RedisEnabled = previousRedisEnabled
	})
}

func useChannelConcurrencyRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	previousRedisEnabled := common.RedisEnabled
	previousRedisClient := common.RDB
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	require.NoError(t, client.Ping(context.Background()).Err())
	common.RedisEnabled = true
	common.RDB = client
	t.Cleanup(func() {
		_ = client.Close()
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRedisClient
	})
	return server, client
}

func TestMemoryChannelConcurrencyEnforcesLimitAtomically(t *testing.T) {
	useMemoryChannelConcurrency(t)

	const attempts = 12
	results := make(chan *ChannelConcurrencyLease, attempts)
	errorsCh := make(chan error, attempts)
	var workers sync.WaitGroup
	for i := 0; i < attempts; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			lease, _, err := AcquireChannelConcurrency(context.Background(), 7, 2, "")
			results <- lease
			errorsCh <- err
		}()
	}
	workers.Wait()
	close(results)
	close(errorsCh)

	leases := make([]*ChannelConcurrencyLease, 0, 2)
	for lease := range results {
		if lease != nil {
			leases = append(leases, lease)
		}
	}
	limited := 0
	for err := range errorsCh {
		if errors.Is(err, ErrChannelConcurrencyLimit) {
			limited++
		}
	}
	require.Len(t, leases, 2)
	assert.Equal(t, attempts-2, limited)

	counts, err := GetChannelConcurrencyCounts(context.Background(), []int{7, 8})
	require.NoError(t, err)
	assert.Equal(t, 2, counts[7])
	assert.Zero(t, counts[8])

	for _, lease := range leases {
		lease.Release()
	}
	counts, err = GetChannelConcurrencyCounts(context.Background(), []int{7})
	require.NoError(t, err)
	assert.Zero(t, counts[7])
}

func TestMemoryChannelConcurrencyTracksUnlimitedChannels(t *testing.T) {
	useMemoryChannelConcurrency(t)

	first, _, err := AcquireChannelConcurrency(context.Background(), 11, 0, "first")
	require.NoError(t, err)
	second, _, err := AcquireChannelConcurrency(context.Background(), 11, 0, "second")
	require.NoError(t, err)
	t.Cleanup(first.Release)
	t.Cleanup(second.Release)

	counts, err := GetChannelConcurrencyCounts(context.Background(), []int{11})
	require.NoError(t, err)
	assert.Equal(t, 2, counts[11])
}

func TestRedisChannelConcurrencySharesLimitAndPrunesExpiredOrphans(t *testing.T) {
	_, client := useChannelConcurrencyRedis(t)

	first, count, err := AcquireChannelConcurrency(context.Background(), 21, 1, "node-a")
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	_, count, err = AcquireChannelConcurrency(context.Background(), 21, 1, "node-b")
	assert.ErrorIs(t, err, ErrChannelConcurrencyLimit)
	assert.Equal(t, 1, count)
	first.Release()

	require.NoError(t, client.ZAdd(context.Background(), channelConcurrencyKey(21), &redis.Z{
		Score:  0,
		Member: "orphan",
	}).Err())

	counts, err := GetChannelConcurrencyCounts(context.Background(), []int{21})
	require.NoError(t, err)
	assert.Zero(t, counts[21])

	replacement, _, err := AcquireChannelConcurrency(context.Background(), 21, 1, "node-c")
	require.NoError(t, err)
	replacement.Release()
}

func TestRedisChannelConcurrencyFailsClosed(t *testing.T) {
	_, client := useChannelConcurrencyRedis(t)
	require.NoError(t, client.Close())

	_, _, err := AcquireChannelConcurrency(context.Background(), 31, 1, "request")
	assert.ErrorIs(t, err, ErrChannelConcurrencyStore)
	_, err = GetChannelConcurrencyCounts(context.Background(), []int{31})
	assert.ErrorIs(t, err, ErrChannelConcurrencyStore)
}
