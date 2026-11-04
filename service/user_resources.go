package service

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/setting"
)

type UserResourceUsage struct {
	Concurrency int `json:"concurrency"`
	InputTPM    int `json:"input_tpm"`
	RPM         int `json:"rpm"`
}
type UserResourceRejection struct {
	Code       string
	RetryAfter int
}

func (e *UserResourceRejection) Error() string { return e.Code }

// Window and slots have independent lifetimes. Reading never extends a slot.
const userResourcesScript = `
redis.replicate_commands()
local t = redis.call('TIME')
local now = t[1]*1000 + math.floor(t[2]/1000)
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
local expired = redis.call('ZRANGEBYSCORE', KEYS[2], '-inf', now-60000)
local total = tonumber(redis.call('HGET', KEYS[3], '_total') or '0')
for _, id in ipairs(expired) do
 total = total - tonumber(redis.call('HGET', KEYS[3], id) or '0')
 redis.call('HDEL', KEYS[3], id)
end
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now-60000)
local concurrent = redis.call('ZCARD', KEYS[1])
local rpm = redis.call('ZCARD', KEYS[2])
if rpm == 0 then total = 0 end
redis.call('HSET', KEYS[3], '_total', total)
redis.call('PEXPIRE', KEYS[3], 60000)
local reason = 0
local retry = 0
local tokens = tonumber(ARGV[2])
if ARGV[1] ~= '' then
 redis.call('ZADD', KEYS[4], now+1800000, ARGV[6])
 redis.call('PEXPIRE', KEYS[4], 1800000)
 if tonumber(ARGV[3]) >= 0 and concurrent >= tonumber(ARGV[3]) then reason = 1
 elseif tonumber(ARGV[4]) >= 0 and total + tokens > tonumber(ARGV[4]) then reason = 2
 elseif tonumber(ARGV[5]) >= 0 and rpm >= tonumber(ARGV[5]) then reason = 3 end
 if reason == 2 or reason == 3 then
  local entries = redis.call('ZRANGE', KEYS[2], 0, -1, 'WITHSCORES')
  local remainingTokens = total
  local remainingRequests = rpm
  for i=1,#entries,2 do
   remainingTokens = remainingTokens - tonumber(redis.call('HGET', KEYS[3], entries[i]) or '0')
   remainingRequests = remainingRequests - 1
   if (reason == 2 and remainingTokens + tokens <= tonumber(ARGV[4])) or (reason == 3 and remainingRequests < tonumber(ARGV[5])) then
    retry = math.max(1, math.ceil((tonumber(entries[i+1])+60000-now)/1000))
    break
   end
  end
 end
 if reason == 0 then
  redis.call('ZADD', KEYS[1], now+1800000, ARGV[1])
  redis.call('PEXPIRE', KEYS[1], 1800000)
  redis.call('ZADD', KEYS[2], now, ARGV[1])
  redis.call('PEXPIRE', KEYS[2], 60000)
  redis.call('HSET', KEYS[3], ARGV[1], tokens, '_total', total+tokens)
  concurrent = concurrent+1
  rpm = rpm+1
  total = total+tokens
 end
end
return {reason,retry,concurrent,total,rpm}
`

type resourceEvent struct {
	at     time.Time
	tokens int
}
type memoryUserResources struct {
	slots  map[string]time.Time
	events []resourceEvent
}

var userResourcesMemory = struct {
	sync.Mutex
	users     map[int]*memoryUserResources
	lastSweep time.Time
}{users: map[int]*memoryUserResources{}}

func (m *memoryUserResources) prune(now time.Time) UserResourceUsage {
	for id, expiry := range m.slots {
		if !expiry.After(now) {
			delete(m.slots, id)
		}
	}
	first := 0
	for first < len(m.events) && !m.events[first].at.Add(time.Minute).After(now) {
		first++
	}
	m.events = m.events[first:]
	u := UserResourceUsage{Concurrency: len(m.slots), RPM: len(m.events)}
	for _, e := range m.events {
		u.InputTPM += e.tokens
	}
	return u
}
func resourceLimitValue(l setting.ResourceLimit) int {
	if !l.Enabled {
		return -1
	}
	return l.Limit
}
func userResourceKeys(id int) []string {
	base := "user_resources:v1:" + strconv.Itoa(id)
	return []string{base + ":slots", base + ":window", base + ":tokens", "user_resources:v1:active"}
}
func userResources(ctx context.Context, id int, member string, tokens int, limits setting.UserResourceLimits, now time.Time) (UserResourceUsage, *UserResourceRejection, error) {
	var u UserResourceUsage
	if common.RedisEnabled {
		if common.RDB == nil {
			return u, nil, fmt.Errorf("resource store unavailable")
		}
		ctx, cancel := context.WithTimeout(ctx, channelRedisTimeout)
		defer cancel()
		values, err := common.RDB.Eval(ctx, userResourcesScript, userResourceKeys(id), member, tokens, resourceLimitValue(limits.Concurrency), resourceLimitValue(limits.InputTPM), resourceLimitValue(limits.RPM), id).Int64Slice()
		if err != nil {
			return u, nil, err
		}
		if len(values) != 5 {
			return u, nil, fmt.Errorf("invalid resource store response")
		}
		u = UserResourceUsage{Concurrency: int(values[2]), InputTPM: int(values[3]), RPM: int(values[4])}
		if values[0] > 0 && values[0] <= 3 {
			return u, &UserResourceRejection{Code: []string{"", "user_concurrency_exceeded", "user_input_tpm_exceeded", "user_rpm_exceeded"}[values[0]], RetryAfter: int(values[1])}, nil
		}
		return u, nil, nil
	}
	userResourcesMemory.Lock()
	defer userResourcesMemory.Unlock()
	if now.Sub(userResourcesMemory.lastSweep) >= time.Minute {
		for key, m := range userResourcesMemory.users {
			v := m.prune(now)
			if v.Concurrency == 0 && v.RPM == 0 {
				delete(userResourcesMemory.users, key)
			}
		}
		userResourcesMemory.lastSweep = now
	}
	m := userResourcesMemory.users[id]
	if m == nil {
		m = &memoryUserResources{slots: map[string]time.Time{}}
		userResourcesMemory.users[id] = m
	}
	u = m.prune(now)
	if member == "" {
		return u, nil, nil
	}
	code := ""
	switch {
	case limits.Concurrency.Enabled && u.Concurrency >= limits.Concurrency.Limit:
		code = "user_concurrency_exceeded"
	case limits.InputTPM.Enabled && tokens > limits.InputTPM.Limit-u.InputTPM:
		code = "user_input_tpm_exceeded"
	case limits.RPM.Enabled && u.RPM >= limits.RPM.Limit:
		code = "user_rpm_exceeded"
	}
	if code != "" {
		retry := 0
		remaining := u
		if code != "user_concurrency_exceeded" {
			for _, e := range m.events {
				remaining.InputTPM -= e.tokens
				remaining.RPM--
				if (code == "user_input_tpm_exceeded" && tokens <= limits.InputTPM.Limit-remaining.InputTPM) || (code == "user_rpm_exceeded" && remaining.RPM < limits.RPM.Limit) {
					retry = max(1, int((e.at.Add(time.Minute).Sub(now)+time.Second-1)/time.Second))
					break
				}
			}
		}
		return u, &UserResourceRejection{Code: code, RetryAfter: retry}, nil
	}
	m.slots[member] = now.Add(30 * time.Minute)
	m.events = append(m.events, resourceEvent{at: now, tokens: tokens})
	u.Concurrency++
	u.RPM++
	u.InputTPM += tokens
	return u, nil, nil
}

// AcquireUserResources is called once before the retry loop; completion only releases concurrency.
func AcquireUserResources(ctx context.Context, id, tokens int, limits setting.UserResourceLimits) (func(), *UserResourceRejection, error) {
	if id <= 0 || tokens < 0 {
		return nil, nil, fmt.Errorf("invalid user resource input")
	}
	member := common.NewRequestId()
	_, rejection, err := userResources(ctx, id, member, tokens, limits, time.Now())
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("user resource admission unavailable: user_id=%d error=%v", id, err))
		if limits.Restricted() {
			recordUserResourceEvent(id, "store_unavailable")
			return nil, nil, err
		}
		recordUserResourceEvent(id, "")
		return func() {}, nil, nil
	}
	if rejection != nil {
		recordUserResourceEvent(id, rejection.Code)
		return nil, rejection, nil
	}
	recordUserResourceEvent(id, "")
	shared := common.RedisEnabled
	keys := userResourceKeys(id)
	var once sync.Once
	return func() {
		once.Do(func() {
			if shared {
				releaseCtx, cancel := redisTimeoutContext()
				defer cancel()
				if err := common.RDB.ZRem(releaseCtx, keys[0], member).Err(); err != nil {
					logger.LogError(releaseCtx, fmt.Sprintf("user resource release failed: user_id=%d error=%v", id, err))
				}
			} else {
				userResourcesMemory.Lock()
				defer userResourcesMemory.Unlock()
				if m := userResourcesMemory.users[id]; m != nil {
					delete(m.slots, member)
				}
			}
		})
	}, nil, nil
}
func GetUserResourceUsage(ctx context.Context, id int) (UserResourceUsage, error) {
	u, _, err := userResources(ctx, id, "", 0, setting.UserResourceLimits{}, time.Now())
	return u, err
}
