package service

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/setting"
	"github.com/go-redis/redis/v8"
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

// A logical admission captures both rules once, before channel retries.
type UserResourceAdmission struct {
	Total     setting.UserResourceLimits
	ModelName string
	Model     *setting.UserResourceLimits
}

func (a UserResourceAdmission) Restricted() bool {
	return a.Total.Restricted() || (a.Model != nil && a.Model.Restricted())
}

// Redis TIME is shared by both buckets. Check every rule before appending any
// admission. Pruning is allowed on rejection; rejection never reserves capacity.
// A repeated member (including a redis-client retry after a lost reply) is a no-op.
const userResourcesScript = `
redis.replicate_commands()
local t=redis.call('TIME')
local now=t[1]*1000+math.floor(t[2]/1000)
local count=tonumber(ARGV[3])
local tokens=tonumber(ARGV[2])
local buckets={}
for b=1,count do
 local k=(b-1)*3
 redis.call('ZREMRANGEBYSCORE',KEYS[k+1],'-inf',now)
 local expired=redis.call('ZRANGEBYSCORE',KEYS[k+2],'-inf',now-60000)
 local total=tonumber(redis.call('HGET',KEYS[k+3],'_total') or '0')
 for _,id in ipairs(expired) do
  total=total-tonumber(redis.call('HGET',KEYS[k+3],id) or '0')
  redis.call('HDEL',KEYS[k+3],id)
 end
 redis.call('ZREMRANGEBYSCORE',KEYS[k+2],'-inf',now-60000)
 local rpm=redis.call('ZCARD',KEYS[k+2])
 if rpm==0 then total=0 end
 if rpm>0 then redis.call('HSET',KEYS[k+3],'_total',total) end
 buckets[b]={redis.call('ZCARD',KEYS[k+1]),total,rpm}
end
if ARGV[1]=='' then
 local result={0,0}
 for _,u in ipairs(buckets) do for _,v in ipairs(u) do table.insert(result,v) end end
 return result
end
-- A slot outlives its window, and is present throughout automatic retries.
if redis.call('ZSCORE',KEYS[1],ARGV[1]) then
 local result={0,0}
 for _,u in ipairs(buckets) do for _,v in ipairs(u) do table.insert(result,v) end end
 return result
end
local reason=0
local retry=0
local unknown=false
for b=1,count do
 local u=buckets[b]
 local k=(b-1)*3
 local a=6+(b-1)*3
 for resource=1,3 do
  local limit=tonumber(ARGV[a+resource])
  local exceeded=limit>=0 and (limit==0 or (resource==2 and u[2]+tokens>limit) or (resource~=2 and u[resource]>=limit))
  if exceeded then
   if reason==0 then reason=(b-1)*3+resource end
   if resource==1 or limit==0 or (resource==2 and tokens>limit) then unknown=true
   else
    local entries=redis.call('ZRANGE',KEYS[k+2],0,-1,'WITHSCORES')
    local remaining=u[resource]
    for i=1,#entries,2 do
     if resource==2 then remaining=remaining-tonumber(redis.call('HGET',KEYS[k+3],entries[i]) or '0')
     else remaining=remaining-1 end
     if (resource==2 and remaining+tokens<=limit) or (resource==3 and remaining<limit) then
      retry=math.max(retry,1,math.ceil((tonumber(entries[i+1])+60000-now)/1000))
      break
     end
    end
   end
  end
 end
end
-- Rejected combinations also need discovery so denial/zero rules are visible.
redis.call('ZADD',KEYS[count*3+1],now+1800000,ARGV[4])
redis.call('PEXPIRE',KEYS[count*3+1],1800000)
if count==2 then
 redis.call('ZADD',KEYS[count*3+2],now+1800000,ARGV[5])
 redis.call('PEXPIRE',KEYS[count*3+2],1800000)
 redis.call('ZADD',KEYS[count*3+3],now+1800000,ARGV[6])
 redis.call('PEXPIRE',KEYS[count*3+3],1800000)
end
if reason==0 then
 for b=1,count do
  local k=(b-1)*3
  redis.call('ZADD',KEYS[k+1],now+1800000,ARGV[1])
  redis.call('PEXPIRE',KEYS[k+1],1800000)
  redis.call('ZADD',KEYS[k+2],now,ARGV[1])
  redis.call('PEXPIRE',KEYS[k+2],60000)
  redis.call('HSET',KEYS[k+3],ARGV[1],tokens,'_total',buckets[b][2]+tokens)
  redis.call('PEXPIRE',KEYS[k+3],60000)
  buckets[b][1]=buckets[b][1]+1
  buckets[b][2]=buckets[b][2]+tokens
  buckets[b][3]=buckets[b][3]+1
 end
end
if unknown then retry=0 end
local result={reason,retry}
for _,u in ipairs(buckets) do for _,v in ipairs(u) do table.insert(result,v) end end
return result
`

var userResourcesRedisScript = redis.NewScript(userResourcesScript)
var userResourceRejectionCodes = []string{"", "user_concurrency_exceeded", "user_input_tpm_exceeded", "user_rpm_exceeded", "user_model_concurrency_exceeded", "user_model_input_tpm_exceeded", "user_model_rpm_exceeded"}

type resourceEvent struct {
	at     time.Time
	tokens int
	member string
}
type memoryUserResources struct {
	slots    map[string]time.Time
	events   []resourceEvent
	lastSeen time.Time
}
type userModelResourceKey struct {
	UserID int
	Model  string
}

var userResourcesMemory = struct {
	sync.Mutex
	users     map[int]*memoryUserResources
	models    map[userModelResourceKey]*memoryUserResources
	lastSweep time.Time
}{users: map[int]*memoryUserResources{}, models: map[userModelResourceKey]*memoryUserResources{}}

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
	clear(m.events[:first])
	m.events = m.events[first:]
	if len(m.events) == 0 {
		m.events = nil
	}
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
func userModelResourceKeys(key userModelResourceKey) []string {
	base := "user_resources:v1:" + strconv.Itoa(key.UserID) + ":model:" + base64.RawURLEncoding.EncodeToString([]byte(key.Model))
	return []string{base + ":slots", base + ":window", base + ":tokens"}
}

const userModelActiveKey = "user_resources:v1:models:active"

func userModelsActiveKey(id int) string {
	return "user_resources:v1:" + strconv.Itoa(id) + ":models:active"
}
func userModelMember(key userModelResourceKey) string {
	return strconv.Itoa(key.UserID) + ":" + base64.RawURLEncoding.EncodeToString([]byte(key.Model))
}

func userResources(ctx context.Context, id int, member string, tokens int, admission UserResourceAdmission, now time.Time) ([]UserResourceUsage, *UserResourceRejection, error) {
	rules := []setting.UserResourceLimits{admission.Total}
	keys := userResourceKeys(id)
	keys = append([]string{}, keys[:3]...)
	if admission.Model != nil {
		rules = append(rules, *admission.Model)
		keys = append(keys, userModelResourceKeys(userModelResourceKey{id, admission.ModelName})...)
	}
	keys = append(keys, userResourceKeys(id)[3])
	if admission.Model != nil {
		keys = append(keys, userModelActiveKey, userModelsActiveKey(id))
	}
	if common.RedisEnabled {
		if common.RDB == nil {
			return nil, nil, fmt.Errorf("resource store unavailable")
		}
		readCtx, cancel := context.WithTimeout(ctx, channelRedisTimeout)
		defer cancel()
		args := []any{member, tokens, len(rules), id, userModelMember(userModelResourceKey{id, admission.ModelName}), admission.ModelName}
		for _, l := range rules {
			args = append(args, resourceLimitValue(l.Concurrency), resourceLimitValue(l.InputTPM), resourceLimitValue(l.RPM))
		}
		values, err := userResourcesRedisScript.Run(readCtx, common.RDB, keys, args...).Int64Slice()
		if err != nil {
			return nil, nil, err
		}
		if len(values) != 2+3*len(rules) {
			return nil, nil, fmt.Errorf("invalid resource store response")
		}
		usage := make([]UserResourceUsage, len(rules))
		for i := range usage {
			usage[i] = UserResourceUsage{int(values[2+3*i]), int(values[3+3*i]), int(values[4+3*i])}
		}
		if values[0] > 0 && values[0] < int64(len(userResourceRejectionCodes)) {
			return usage, &UserResourceRejection{userResourceRejectionCodes[values[0]], int(values[1])}, nil
		}
		if values[0] != 0 {
			return nil, nil, fmt.Errorf("invalid resource rejection")
		}
		return usage, nil, nil
	}
	userResourcesMemory.Lock()
	defer userResourcesMemory.Unlock()
	if now.Sub(userResourcesMemory.lastSweep) >= time.Minute {
		for key, m := range userResourcesMemory.users {
			u := m.prune(now)
			if u.Concurrency == 0 && u.RPM == 0 {
				delete(userResourcesMemory.users, key)
			}
		}
		for key, m := range userResourcesMemory.models {
			u := m.prune(now)
			if u.Concurrency == 0 && u.RPM == 0 && !m.lastSeen.Add(30*time.Minute).After(now) {
				delete(userResourcesMemory.models, key)
			}
		}
		userResourcesMemory.lastSweep = now
	}
	buckets := []*memoryUserResources{userResourcesMemory.users[id]}
	if buckets[0] == nil {
		buckets[0] = &memoryUserResources{slots: map[string]time.Time{}}
		if member != "" {
			userResourcesMemory.users[id] = buckets[0]
		}
	}
	if admission.Model != nil {
		key := userModelResourceKey{id, admission.ModelName}
		m := userResourcesMemory.models[key]
		if m == nil {
			m = &memoryUserResources{slots: map[string]time.Time{}}
			if member != "" {
				userResourcesMemory.models[key] = m
			}
		}
		buckets = append(buckets, m)
	}
	usage := make([]UserResourceUsage, len(buckets))
	for i, m := range buckets {
		usage[i] = m.prune(now)
		if member != "" {
			m.lastSeen = now
		}
	}
	if member == "" {
		return usage, nil, nil
	}
	if _, exists := buckets[0].slots[member]; exists {
		return usage, nil, nil
	}
	reason, retry, unknown := 0, 0, false
	for i, l := range rules {
		u := usage[i]
		for r, limit := range []setting.ResourceLimit{l.Concurrency, l.InputTPM, l.RPM} {
			if !limit.Enabled {
				continue
			}
			exceeded := limit.Limit == 0
			switch r {
			case 0:
				exceeded = exceeded || u.Concurrency >= limit.Limit
			case 1:
				exceeded = exceeded || tokens > limit.Limit-u.InputTPM
			case 2:
				exceeded = exceeded || u.RPM >= limit.Limit
			}
			if !exceeded {
				continue
			}
			if reason == 0 {
				reason = 1 + i*3 + r
			}
			if r == 0 || limit.Limit == 0 || (r == 1 && tokens > limit.Limit) {
				unknown = true
				continue
			}
			remaining := u
			for _, e := range buckets[i].events {
				remaining.InputTPM -= e.tokens
				remaining.RPM--
				if (r == 1 && tokens <= limit.Limit-remaining.InputTPM) || (r == 2 && remaining.RPM < limit.Limit) {
					retry = max(retry, 1, int((e.at.Add(time.Minute).Sub(now)+time.Second-1)/time.Second))
					break
				}
			}
		}
	}
	if reason != 0 {
		if unknown {
			retry = 0
		}
		return usage, &UserResourceRejection{userResourceRejectionCodes[reason], retry}, nil
	}
	for i, m := range buckets {
		m.slots[member] = now.Add(30 * time.Minute)
		m.events = append(m.events, resourceEvent{now, tokens, member})
		usage[i].Concurrency++
		usage[i].RPM++
		usage[i].InputTPM += tokens
	}
	return usage, nil, nil
}

// Completion releases only slots captured at admission; subsequent config changes
// never change that set. Store failures do not cancel admitted business requests.
func AcquireUserResources(ctx context.Context, id, tokens int, admission UserResourceAdmission) (func(), *UserResourceRejection, error) {
	if id <= 0 || tokens < 0 || (admission.Model != nil && admission.ModelName == "") {
		return nil, nil, fmt.Errorf("invalid user resource input")
	}
	member := common.NewRequestId()
	_, rejection, err := userResources(ctx, id, member, tokens, admission, time.Now())
	reason := ""
	if rejection != nil {
		reason = rejection.Code
	}
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("user resource admission unavailable: user_id=%d error=%v", id, err))
		if admission.Restricted() {
			reason = "store_unavailable"
		}
	}
	recordUserResourceEvent(id, reason)
	if admission.Model != nil {
		recordUserModelResourceEvent(userModelResourceKey{id, admission.ModelName}, reason, time.Now())
	}
	if err != nil {
		if admission.Restricted() {
			return nil, nil, err
		}
		return func() {}, nil, nil
	}
	if rejection != nil {
		return nil, rejection, nil
	}
	shared, client := common.RedisEnabled, common.RDB
	slotKeys := []string{userResourceKeys(id)[0]}
	if admission.Model != nil {
		slotKeys = append(slotKeys, userModelResourceKeys(userModelResourceKey{id, admission.ModelName})[0])
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			if shared {
				releaseCtx, cancel := redisTimeoutContext()
				defer cancel()
				// One command removes the same member from both captured sets atomically.
				if err := client.Eval(releaseCtx, `for _,key in ipairs(KEYS) do redis.call('ZREM',key,ARGV[1]) end return 1`, slotKeys, member).Err(); err != nil {
					logger.LogError(releaseCtx, fmt.Sprintf("user resource release failed: user_id=%d error=%v", id, err))
				}
			} else {
				userResourcesMemory.Lock()
				defer userResourcesMemory.Unlock()
				if m := userResourcesMemory.users[id]; m != nil {
					delete(m.slots, member)
				}
				if admission.Model != nil {
					if m := userResourcesMemory.models[userModelResourceKey{id, admission.ModelName}]; m != nil {
						delete(m.slots, member)
					}
				}
			}
		})
	}, nil, nil
}
func GetUserResourceUsage(ctx context.Context, id int) (UserResourceUsage, error) {
	usage, _, err := userResources(ctx, id, "", 0, UserResourceAdmission{}, time.Now())
	if err != nil {
		return UserResourceUsage{}, err
	}
	return usage[0], nil
}
