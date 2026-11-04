package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/go-redis/redis/v8"
)

const (
	channelConcurrencyKeyPrefix = "channel_concurrency:v1:"
	channelLeaseDuration        = 30 * time.Minute
	channelRedisTimeout         = 3 * time.Second
	channelRedisKeyTTL          = 2 * channelLeaseDuration
)

var (
	ErrChannelConcurrencyLimit = errors.New("channel concurrency limit exceeded")
	ErrChannelConcurrencyStore = errors.New("channel concurrency store unavailable")

	memoryChannelConcurrency = struct {
		sync.Mutex
		slots map[int]map[string]time.Time
	}{slots: make(map[int]map[string]time.Time)}
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
	released  sync.Once
}

func channelConcurrencyKey(channelID int) string {
	return channelConcurrencyKeyPrefix + strconv.Itoa(channelID)
}

func redisTimeoutContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), channelRedisTimeout)
}

func AcquireChannelConcurrency(parent context.Context, channelID int, maximum int, member string) (result *ChannelConcurrencyLease, count int, resultErr error) {
	defer func() {
		if maximum == 0 && errors.Is(resultErr, ErrChannelConcurrencyStore) {
			logger.LogError(parent, fmt.Sprintf("unlimited channel statistics unavailable: channel_id=%d error=%v", channelID, resultErr))
			ctx, cancel := context.WithCancel(parent)
			result = &ChannelConcurrencyLease{ctx: ctx, cancel: cancel}
			count = -1
			resultErr = nil
		}
	}()
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
		current := pruneMemoryChannelSlots(channelID, time.Now())
		if maximum > 0 && current >= maximum {
			memoryChannelConcurrency.Unlock()
			cancel()
			return nil, current, ErrChannelConcurrencyLimit
		}
		current++
		if memoryChannelConcurrency.slots[channelID] == nil {
			memoryChannelConcurrency.slots[channelID] = map[string]time.Time{}
		}
		memoryChannelConcurrency.slots[channelID][member] = time.Now().Add(channelLeaseDuration)
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

func (lease *ChannelConcurrencyLease) Release() {
	if lease == nil {
		return
	}
	lease.released.Do(func() {
		if lease.redis {
			ctx, cancel := redisTimeoutContext()
			_, err := common.RDB.Eval(ctx, releaseChannelLeaseScript,
				[]string{channelConcurrencyKey(lease.channelID)}, lease.member).Result()
			cancel()
			if err != nil {
				logger.LogError(lease.ctx, fmt.Sprintf("failed to release channel concurrency lease: channel_id=%d error=%v", lease.channelID, err))
			}
		} else {
			memoryChannelConcurrency.Lock()
			delete(memoryChannelConcurrency.slots[lease.channelID], lease.member)
			if len(memoryChannelConcurrency.slots[lease.channelID]) == 0 {
				delete(memoryChannelConcurrency.slots, lease.channelID)
			}
			memoryChannelConcurrency.Unlock()
		}
		lease.cancel()
	})
}

func GetChannelConcurrencyCounts(parent context.Context, channelIDs []int) (map[int]int, error) {
	ctx, cancel := context.WithTimeout(parent, channelRedisTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	counts := make(map[int]int, len(channelIDs))
	if !common.RedisEnabled {
		memoryChannelConcurrency.Lock()
		for _, channelID := range channelIDs {
			counts[channelID] = pruneMemoryChannelSlots(channelID, time.Now())
		}
		memoryChannelConcurrency.Unlock()
		return counts, nil
	}
	if common.RDB == nil {
		return nil, fmt.Errorf("%w: Redis client is not initialized", ErrChannelConcurrencyStore)
	}

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

// Caller holds the memory mutex. Expiry never cancels a running request.
func pruneMemoryChannelSlots(id int, now time.Time) int {
	for member, expiry := range memoryChannelConcurrency.slots[id] {
		if !expiry.After(now) {
			delete(memoryChannelConcurrency.slots[id], member)
		}
	}
	if len(memoryChannelConcurrency.slots[id]) == 0 {
		delete(memoryChannelConcurrency.slots, id)
	}
	return len(memoryChannelConcurrency.slots[id])
}
