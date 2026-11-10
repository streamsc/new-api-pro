package service

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/go-redis/redis/v8"
)

const resourceBatchSize = 100
const resourceDiscoveryLimit = 10000
const userModelCounterIdle = 24 * time.Hour

type userModelEvents struct {
	LastSeen time.Time
	Admitted uint64
	Rejected map[string]uint64
}

var userModelResourceEvents = struct {
	sync.Mutex
	events    map[userModelResourceKey]*userModelEvents
	lastSweep time.Time
}{events: map[userModelResourceKey]*userModelEvents{}}

func recordUserModelResourceEvent(key userModelResourceKey, reason string, now time.Time) {
	userModelResourceEvents.Lock()
	defer userModelResourceEvents.Unlock()
	if now.Sub(userModelResourceEvents.lastSweep) >= time.Minute {
		for k, e := range userModelResourceEvents.events {
			if !e.LastSeen.Add(userModelCounterIdle).After(now) {
				delete(userModelResourceEvents.events, k)
			}
		}
		userModelResourceEvents.lastSweep = now
	}
	e := userModelResourceEvents.events[key]
	if e == nil || !e.LastSeen.Add(userModelCounterIdle).After(now) {
		e = &userModelEvents{Rejected: map[string]uint64{}}
		userModelResourceEvents.events[key] = e
	}
	e.LastSeen = now
	if reason == "" {
		e.Admitted++
	} else {
		e.Rejected[reason]++
	}
}

var resourceActiveScript = redis.NewScript(`
redis.replicate_commands()
local t=redis.call('TIME')
local now=t[1]*1000+math.floor(t[2]/1000)
local expired=redis.call('ZRANGEBYSCORE',KEYS[1],'-inf',now,'LIMIT',0,1000)
if #expired>0 then redis.call('ZREM',KEYS[1],unpack(expired)) end
return redis.call('ZRANGEBYSCORE',KEYS[1],'('..now,'+inf','LIMIT',0,tonumber(ARGV[1])+1)
`)

// Discovery is bounded and never scans Redis's keyspace. An error means the
// directory may be incomplete, independently of successful reads of known rows.
func discoverUserModels(ctx context.Context, config setting.UserResourcesConfig, userID int) ([]userModelResourceKey, bool) {
	keys := map[userModelResourceKey]struct{}{}
	complete := true
	if common.RedisEnabled {
		if common.RDB == nil {
			complete = false
		} else {
			index := userModelActiveKey
			if userID > 0 {
				index = userModelsActiveKey(userID)
			}
			readCtx, cancel := context.WithTimeout(ctx, channelRedisTimeout)
			entries, err := resourceActiveScript.Run(readCtx, common.RDB, []string{index}, resourceDiscoveryLimit).StringSlice()
			cancel()
			if err != nil {
				complete = false
			} else {
				if len(entries) > resourceDiscoveryLimit {
					entries = entries[:resourceDiscoveryLimit]
					complete = false
				}
				for _, entry := range entries {
					key := userModelResourceKey{UserID: userID, Model: entry}
					if userID == 0 {
						id, encoded, ok := strings.Cut(entry, ":")
						n, e := strconv.Atoi(id)
						name, decodeErr := base64.RawURLEncoding.DecodeString(encoded)
						if !ok || e != nil || n <= 0 || decodeErr != nil {
							complete = false
							continue
						}
						key = userModelResourceKey{n, string(name)}
					}
					keys[key] = struct{}{}
				}
			}
		}
	} else {
		now := time.Now()
		userResourcesMemory.Lock()
		scanned := 0
		for key, m := range userResourcesMemory.models {
			if scanned >= resourceDiscoveryLimit || ctx.Err() != nil {
				complete = false
				break
			}
			scanned++
			if !m.lastSeen.Add(30 * time.Minute).After(now) {
				u := m.prune(now)
				if u.Concurrency == 0 && u.RPM == 0 {
					delete(userResourcesMemory.models, key)
					continue
				}
			}
			if userID == 0 || key.UserID == userID {
				keys[key] = struct{}{}
			}
			if len(keys) > resourceDiscoveryLimit {
				complete = false
				break
			}
		}
		userResourcesMemory.Unlock()
	}
	// Recent local events preserve known rows during store outages, including
	// a rule removed while requests still have a window or slot.
	userModelResourceEvents.Lock()
	scanned := 0
	now := time.Now()
	for key, event := range userModelResourceEvents.events {
		if scanned >= resourceDiscoveryLimit || ctx.Err() != nil {
			complete = false
			break
		}
		scanned++
		if (userID == 0 || key.UserID == userID) && event.LastSeen.Add(30*time.Minute).After(now) {
			keys[key] = struct{}{}
		}
	}
	userModelResourceEvents.Unlock()
	for name, rule := range config.Models {
		if len(keys) > resourceDiscoveryLimit || ctx.Err() != nil {
			complete = false
			break
		}
		if userID > 0 {
			keys[userModelResourceKey{userID, name}] = struct{}{}
		} else {
			for raw := range rule.Users {
				if len(keys) > resourceDiscoveryLimit || ctx.Err() != nil {
					complete = false
					break
				}
				id, _ := strconv.Atoi(raw)
				keys[userModelResourceKey{id, name}] = struct{}{}
			}
		}
	}
	result := make([]userModelResourceKey, 0, len(keys))
	for key := range keys {
		result = append(result, key)
	}
	slices.SortFunc(result, func(a, b userModelResourceKey) int {
		if a.UserID < b.UserID {
			return -1
		}
		if a.UserID > b.UserID {
			return 1
		}
		return strings.Compare(a.Model, b.Model)
	})
	if len(result) > resourceDiscoveryLimit {
		result = result[:resourceDiscoveryLimit]
		complete = false
	}
	return result, complete
}

// Batch reads prune expired records but never extend slot, window or index TTLs.
// Each row is consistent within one Redis execution; no cross-store snapshot is claimed.
const resourceReadBatchLua = `
redis.replicate_commands()
local t=redis.call('TIME')
local now=t[1]*1000+math.floor(t[2]/1000)
local result={}
for k=1,#KEYS,3 do
 redis.call('ZREMRANGEBYSCORE',KEYS[k],'-inf',now)
 local expired=redis.call('ZRANGEBYSCORE',KEYS[k+1],'-inf',now-60000)
 local total=tonumber(redis.call('HGET',KEYS[k+2],'_total') or '0')
 for _,id in ipairs(expired) do
  total=total-tonumber(redis.call('HGET',KEYS[k+2],id) or '0')
  redis.call('HDEL',KEYS[k+2],id)
 end
 redis.call('ZREMRANGEBYSCORE',KEYS[k+1],'-inf',now-60000)
 local rpm=redis.call('ZCARD',KEYS[k+1])
 if rpm==0 then total=0 else redis.call('HSET',KEYS[k+2],'_total',total) end
 table.insert(result,redis.call('ZCARD',KEYS[k]))
 table.insert(result,total)
 table.insert(result,rpm)
end
return result
`

var resourceReadBatchScript = redis.NewScript(resourceReadBatchLua)

func readResourceUsageBatch(ctx context.Context, keys []userModelResourceKey) (map[userModelResourceKey]UserResourceUsage, error) {
	if len(keys) > resourceBatchSize {
		return nil, fmt.Errorf("resource batch too large")
	}
	result := make(map[userModelResourceKey]UserResourceUsage, len(keys))
	if len(keys) == 0 {
		return result, nil
	}
	if !common.RedisEnabled {
		userResourcesMemory.Lock()
		defer userResourcesMemory.Unlock()
		now := time.Now()
		for _, key := range keys {
			m := userResourcesMemory.models[key]
			if key.Model == "" {
				m = userResourcesMemory.users[key.UserID]
			}
			u := UserResourceUsage{}
			if m != nil {
				u = m.prune(now)
			}
			result[key] = u
		}
		return result, nil
	}
	if common.RDB == nil {
		return nil, fmt.Errorf("resource store unavailable")
	}
	redisKeys := make([]string, 0, len(keys)*3)
	for _, key := range keys {
		if key.Model == "" {
			redisKeys = append(redisKeys, userResourceKeys(key.UserID)[:3]...)
		} else {
			redisKeys = append(redisKeys, userModelResourceKeys(key)...)
		}
	}
	readCtx, cancel := context.WithTimeout(ctx, channelRedisTimeout)
	defer cancel()
	// A single bounded Lua batch is one round trip, without per-row calls.
	values, err := resourceReadBatchScript.Run(readCtx, common.RDB, redisKeys).Int64Slice()
	if err != nil {
		return nil, err
	}
	if len(values) != 3*len(keys) {
		return nil, fmt.Errorf("incomplete resource batch")
	}
	for i, key := range keys {
		if values[3*i] < 0 || values[3*i+1] < 0 || values[3*i+2] < 0 {
			return nil, fmt.Errorf("invalid resource usage")
		}
		result[key] = UserResourceUsage{int(values[3*i]), int(values[3*i+1]), int(values[3*i+2])}
	}
	return result, nil
}

type UserModelResourceRow struct {
	UserID     int                        `json:"user_id"`
	Model      string                     `json:"model"`
	Configured bool                       `json:"configured"`
	Limits     setting.UserResourceLimits `json:"limits"`
	Sources    map[string]string          `json:"sources"`
	Usage      *UserResourceUsage         `json:"usage,omitempty"`
	Available  bool                       `json:"available"`
}

func modelResourceRow(config setting.UserResourcesConfig, key userModelResourceKey) UserModelResourceRow {
	row := UserModelResourceRow{UserID: key.UserID, Model: key.Model, Sources: map[string]string{"concurrency": "unconfigured", "input_tpm": "unconfigured", "rpm": "unconfigured"}}
	if limits := config.ForModel(key.UserID, key.Model); limits != nil {
		row.Configured = true
		row.Limits = *limits
		row.Sources = setting.ResourceSources(config.Models[key.Model].Users[strconv.Itoa(key.UserID)], "model_default")
	}
	return row
}
func GetUserModelResourcePage(ctx context.Context, config setting.UserResourcesConfig, userID int, model string, page, pageSize int) ([]UserModelResourceRow, int, bool) {
	keys, complete := discoverUserModels(ctx, config, userID)
	if model != "" {
		keys = slices.DeleteFunc(keys, func(k userModelResourceKey) bool { return k.Model != model })
	}
	total := len(keys)
	start := min((page-1)*pageSize, total)
	keys = keys[start:min(start+pageSize, total)]
	usage, err := readResourceUsageBatch(ctx, keys)
	rows := make([]UserModelResourceRow, 0, len(keys))
	for _, key := range keys {
		row := modelResourceRow(config, key)
		if err == nil {
			v := usage[key]
			row.Usage = &v
			row.Available = true
		}
		rows = append(rows, row)
	}
	return rows, total, complete
}
