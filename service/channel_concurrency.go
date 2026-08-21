package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/go-redis/redis/v8"
)

const (
	channelConcurrencyKeyPrefix = "channel_concurrency:v1:"
	channelLeaseDuration        = 120 * time.Second
	channelLeaseRenewInterval   = 30 * time.Second
	channelLeaseSafetyWindow    = 30 * time.Second
	channelRedisTimeout         = 3 * time.Second
	channelRedisKeyTTL          = 2 * channelLeaseDuration
)

var (
	ErrChannelConcurrencyLimit = errors.New("channel concurrency limit exceeded")
	ErrChannelConcurrencyStore = errors.New("channel concurrency store unavailable")

	memoryChannelConcurrency = struct {
		sync.Mutex
		counts map[int]int
	}{counts: make(map[int]int)}
)

const acquireChannelLeaseScript = `
local redisTime = redis.call('TIME')
local now = redisTime[1] * 1000 + math.floor(redisTime[2] / 1000)
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
local count = redis.call('ZCARD', KEYS[1])
local maximum = tonumber(ARGV[1])
if maximum > 0 and count >= maximum then
  redis.call('PEXPIRE', KEYS[1], ARGV[4])
  return {0, count}
end
redis.call('ZADD', KEYS[1], now + tonumber(ARGV[2]), ARGV[3])
redis.call('PEXPIRE', KEYS[1], ARGV[4])
return {1, count + 1}
`

const renewChannelLeaseScript = `
if redis.call('ZSCORE', KEYS[1], ARGV[1]) == false then
  return 0
end
local redisTime = redis.call('TIME')
local now = redisTime[1] * 1000 + math.floor(redisTime[2] / 1000)
redis.call('ZADD', KEYS[1], 'XX', now + tonumber(ARGV[2]), ARGV[1])
redis.call('PEXPIRE', KEYS[1], ARGV[3])
return 1
`

const releaseChannelLeaseScript = `
redis.call('ZREM', KEYS[1], ARGV[1])
local count = redis.call('ZCARD', KEYS[1])
if count == 0 then
  redis.call('DEL', KEYS[1])
end
return count
`

const countChannelLeasesScript = `
local redisTime = redis.call('TIME')
local now = redisTime[1] * 1000 + math.floor(redisTime[2] / 1000)
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
local count = redis.call('ZCARD', KEYS[1])
if count == 0 then
  redis.call('DEL', KEYS[1])
else
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return count
`

type ChannelConcurrencyLease struct {
	channelID int
	member    string
	redis     bool
	ctx       context.Context
	cancel    context.CancelFunc
	stop      chan struct{}
	done      chan struct{}
	released  sync.Once
	lost      atomic.Bool
}

func channelConcurrencyKey(channelID int) string {
	return channelConcurrencyKeyPrefix + strconv.Itoa(channelID)
}

func redisTimeoutContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), channelRedisTimeout)
}

func AcquireChannelConcurrency(parent context.Context, channelID int, maximum int, member string) (*ChannelConcurrencyLease, int, error) {
	if channelID <= 0 {
		return nil, 0, fmt.Errorf("invalid channel id %d", channelID)
	}
	if maximum < 0 {
		return nil, 0, fmt.Errorf("invalid channel concurrency maximum %d", maximum)
	}
	if member == "" {
		member = common.NewRequestId()
	}

	leaseContext, cancel := context.WithCancel(parent)
	lease := &ChannelConcurrencyLease{
		channelID: channelID,
		member:    member,
		ctx:       leaseContext,
		cancel:    cancel,
	}

	if !common.RedisEnabled {
		memoryChannelConcurrency.Lock()
		current := memoryChannelConcurrency.counts[channelID]
		if maximum > 0 && current >= maximum {
			memoryChannelConcurrency.Unlock()
			cancel()
			return nil, current, ErrChannelConcurrencyLimit
		}
		current++
		memoryChannelConcurrency.counts[channelID] = current
		memoryChannelConcurrency.Unlock()
		return lease, current, nil
	}

	if common.RDB == nil {
		cancel()
		return nil, 0, fmt.Errorf("%w: Redis client is not initialized", ErrChannelConcurrencyStore)
	}
	ctx, stop := redisTimeoutContext()
	defer stop()
	values, err := common.RDB.Eval(ctx, acquireChannelLeaseScript,
		[]string{channelConcurrencyKey(channelID)}, maximum, channelLeaseDuration.Milliseconds(), member, channelRedisKeyTTL.Milliseconds()).Slice()
	if err != nil {
		cancel()
		return nil, 0, fmt.Errorf("%w: %v", ErrChannelConcurrencyStore, err)
	}
	if len(values) != 2 {
		cancel()
		return nil, 0, fmt.Errorf("%w: unexpected acquire reply length %d", ErrChannelConcurrencyStore, len(values))
	}
	allowed, err := redisReplyInt(values[0])
	if err != nil {
		cancel()
		return nil, 0, fmt.Errorf("%w: %v", ErrChannelConcurrencyStore, err)
	}
	current, err := redisReplyInt(values[1])
	if err != nil {
		cancel()
		return nil, 0, fmt.Errorf("%w: %v", ErrChannelConcurrencyStore, err)
	}
	if allowed == 0 {
		cancel()
		return nil, current, ErrChannelConcurrencyLimit
	}

	lease.redis = true
	lease.stop = make(chan struct{})
	lease.done = make(chan struct{})
	go lease.renew()
	return lease, current, nil
}

func redisReplyInt(value any) (int, error) {
	switch typed := value.(type) {
	case int64:
		return int(typed), nil
	case string:
		return strconv.Atoi(typed)
	case []byte:
		return strconv.Atoi(string(typed))
	default:
		return 0, fmt.Errorf("unexpected Redis integer reply type %T", value)
	}
}

func (lease *ChannelConcurrencyLease) Context() context.Context {
	return lease.ctx
}

func (lease *ChannelConcurrencyLease) Lost() bool {
	return lease != nil && lease.lost.Load()
}

func (lease *ChannelConcurrencyLease) renew() {
	defer close(lease.done)
	ticker := time.NewTicker(channelLeaseRenewInterval)
	defer ticker.Stop()
	lastSuccess := time.Now()
	for {
		select {
		case <-lease.stop:
			return
		case <-lease.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := redisTimeoutContext()
			renewed, err := common.RDB.Eval(ctx, renewChannelLeaseScript,
				[]string{channelConcurrencyKey(lease.channelID)}, lease.member,
				channelLeaseDuration.Milliseconds(), channelRedisKeyTTL.Milliseconds()).Int()
			cancel()
			if err == nil && renewed == 1 {
				lastSuccess = time.Now()
				continue
			}
			if err != nil {
				logger.LogError(lease.ctx, fmt.Sprintf("failed to renew channel concurrency lease: channel_id=%d error=%v", lease.channelID, err))
			}
			if (err == nil && renewed == 0) || time.Since(lastSuccess) >= channelLeaseDuration-channelLeaseSafetyWindow {
				lease.lost.Store(true)
				lease.cancel()
				return
			}
		}
	}
}

func (lease *ChannelConcurrencyLease) Release() {
	if lease == nil {
		return
	}
	lease.released.Do(func() {
		if lease.redis {
			close(lease.stop)
			<-lease.done
			ctx, cancel := redisTimeoutContext()
			_, err := common.RDB.Eval(ctx, releaseChannelLeaseScript,
				[]string{channelConcurrencyKey(lease.channelID)}, lease.member).Result()
			cancel()
			if err != nil {
				logger.LogError(lease.ctx, fmt.Sprintf("failed to release channel concurrency lease: channel_id=%d error=%v", lease.channelID, err))
			}
		} else {
			memoryChannelConcurrency.Lock()
			current := memoryChannelConcurrency.counts[lease.channelID]
			if current <= 1 {
				delete(memoryChannelConcurrency.counts, lease.channelID)
			} else {
				memoryChannelConcurrency.counts[lease.channelID] = current - 1
			}
			memoryChannelConcurrency.Unlock()
		}
		lease.cancel()
	})
}

func GetChannelConcurrencyCounts(channelIDs []int) (map[int]int, error) {
	counts := make(map[int]int, len(channelIDs))
	if !common.RedisEnabled {
		memoryChannelConcurrency.Lock()
		for _, channelID := range channelIDs {
			counts[channelID] = memoryChannelConcurrency.counts[channelID]
		}
		memoryChannelConcurrency.Unlock()
		return counts, nil
	}
	if common.RDB == nil {
		return nil, fmt.Errorf("%w: Redis client is not initialized", ErrChannelConcurrencyStore)
	}

	ctx, cancel := redisTimeoutContext()
	defer cancel()
	pipe := common.RDB.Pipeline()
	commands := make(map[int]*redis.Cmd, len(channelIDs))
	for _, channelID := range channelIDs {
		commands[channelID] = pipe.Eval(ctx, countChannelLeasesScript,
			[]string{channelConcurrencyKey(channelID)}, channelRedisKeyTTL.Milliseconds())
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrChannelConcurrencyStore, err)
	}
	for channelID, command := range commands {
		count, err := command.Int()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrChannelConcurrencyStore, err)
		}
		counts[channelID] = count
	}
	return counts, nil
}
