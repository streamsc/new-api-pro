package service

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/prometheus/client_golang/prometheus"
)

var userResourceEvents = struct {
	sync.Mutex
	ids map[int]struct{}
}{ids: map[int]struct{}{}}
var UserResourceAdmitted = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "new_api_user_admitted_requests_total", Help: "Locally admitted logical requests; resets on process restart."}, []string{"user_id"})
var UserResourceRejected = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "new_api_user_rejected_requests_total", Help: "Local admission rejections, including a separate store_unavailable reason."}, []string{"user_id", "reason"})

func recordUserResourceEvent(id int, reason string) {
	userResourceEvents.Lock()
	userResourceEvents.ids[id] = struct{}{}
	userResourceEvents.Unlock()
	labels := []string{strconv.Itoa(id)}
	UserResourceAdmitted.WithLabelValues(labels...).Add(0)
	for _, code := range []string{"user_concurrency_exceeded", "user_input_tpm_exceeded", "user_rpm_exceeded", "store_unavailable"} {
		UserResourceRejected.WithLabelValues(append(labels, code)...).Add(0)
	}
	if reason == "" {
		UserResourceAdmitted.WithLabelValues(labels...).Inc()
	} else {
		UserResourceRejected.WithLabelValues(append(labels, reason)...).Inc()
	}
}

type UserResourcesCollector struct{ Context context.Context }

// Dynamic samples intentionally use an unchecked collector; unavailable usage is omitted.
func (UserResourcesCollector) Describe(chan<- *prometheus.Desc) {}
func (collector UserResourcesCollector) Collect(ch chan<- prometheus.Metric) {
	UserResourceAdmitted.Collect(ch)
	UserResourceRejected.Collect(ch)
	scope := "memory"
	if common.RedisEnabled {
		scope = "redis"
	}
	emitResourceHealth(ch, scope, "counters", collectUserModelCounters(collector.Context, ch))
	config, err := setting.CurrentUserResourcesConfig()
	if err != nil || model.DB == nil {
		emitResourceHealth(ch, scope, "config", false)
		emitResourceHealth(ch, scope, "directory", false)
		emitResourceHealth(ch, scope, "usage", false)
		return
	}
	emitResourceHealth(ch, scope, "config", true)
	complete := true
	userResourceEvents.Lock()
	seen := map[int]struct{}{}
	for id := range userResourceEvents.ids {
		if len(seen) >= resourceDiscoveryLimit || collector.Context.Err() != nil {
			complete = false
			break
		}
		seen[id] = struct{}{}
	}
	userResourceEvents.Unlock()
	if common.RedisEnabled {
		if common.RDB == nil {
			complete = false
		} else {
			readCtx, cancel := context.WithTimeout(collector.Context, channelRedisTimeout)
			active, readErr := resourceActiveScript.Run(readCtx, common.RDB, []string{userResourceKeys(1)[3]}, resourceDiscoveryLimit).StringSlice()
			cancel()
			if readErr != nil {
				complete = false
			} else {
				if len(active) > resourceDiscoveryLimit {
					active = active[:resourceDiscoveryLimit]
					complete = false
				}
				for _, raw := range active {
					id, e := strconv.Atoi(raw)
					if e == nil && id > 0 {
						seen[id] = struct{}{}
					} else {
						complete = false
					}
				}
			}
		}
	}
	for raw := range config.Users {
		if len(seen) >= resourceDiscoveryLimit || collector.Context.Err() != nil {
			complete = false
			break
		}
		id, _ := strconv.Atoi(raw)
		seen[id] = struct{}{}
	}
	modelKeys, modelComplete := discoverUserModels(collector.Context, config, 0)
	complete = complete && modelComplete
	keys := make([]userModelResourceKey, 0, len(seen)+len(modelKeys))
	for id := range seen {
		keys = append(keys, userModelResourceKey{UserID: id})
	}
	keys = append(keys, modelKeys...)
	slices.SortFunc(keys, func(a, b userModelResourceKey) int {
		if a.UserID < b.UserID {
			return -1
		}
		if a.UserID > b.UserID {
			return 1
		}
		return strings.Compare(a.Model, b.Model)
	})
	if len(keys) > resourceDiscoveryLimit {
		keys = keys[:resourceDiscoveryLimit]
		complete = false
	}
	emitResourceHealth(ch, scope, "directory", complete)
	usageComplete := true
	for offset := 0; offset < len(keys); offset += resourceBatchSize {
		if collector.Context.Err() != nil {
			usageComplete = false
			break
		}
		batch := keys[offset:min(offset+resourceBatchSize, len(keys))]
		ids := make([]int, 0, len(batch))
		for _, key := range batch {
			ids = append(ids, key.UserID)
		}
		var users []struct{ Id int }
		if err := model.DB.WithContext(collector.Context).Model(&model.User{}).Select("id").Where("id IN ?", ids).Find(&users).Error; err != nil {
			usageComplete = false
			break
		}
		existing := map[int]bool{}
		for _, user := range users {
			existing[user.Id] = true
		}
		usage, readErr := readResourceUsageBatch(collector.Context, batch)
		if readErr != nil {
			usageComplete = false
		}
		for _, key := range batch {
			if !existing[key.UserID] {
				continue
			}
			limits := config.ForUser(key.UserID)
			prefix := "new_api_user_"
			labelNames := []string{"scope", "user_id"}
			labels := []string{scope, strconv.Itoa(key.UserID)}
			if key.Model != "" {
				limits = modelResourceRow(config, key).Limits
				prefix = "new_api_user_model_"
				labelNames = append(labelNames, "model")
				labels = append(labels, key.Model)
			}
			for resource, limit := range map[string]setting.ResourceLimit{"concurrency": limits.Concurrency, "input_tpm": limits.InputTPM, "rpm": limits.RPM} {
				enabled := 0.
				if limit.Enabled {
					enabled = 1
				}
				names := append(append([]string{}, labelNames...), "resource")
				values := append(append([]string{}, labels...), resource)
				ch <- prometheus.MustNewConstMetric(prometheus.NewDesc(prefix+"limit_enabled", "Whether the effective resource limit is enabled.", names, nil), prometheus.GaugeValue, enabled, values...)
				ch <- prometheus.MustNewConstMetric(prometheus.NewDesc(prefix+"limit", "Effective limit; interpret only when enabled.", names, nil), prometheus.GaugeValue, float64(limit.Limit), values...)
			}
			available := 0.
			if readErr == nil {
				available = 1
			}
			ch <- prometheus.MustNewConstMetric(prometheus.NewDesc(prefix+"usage_available", "Whether this usage was read successfully.", labelNames, nil), prometheus.GaugeValue, available, labels...)
			if readErr != nil {
				continue
			}
			u := usage[key]
			for name, value := range map[string]int{"inflight_requests": u.Concurrency, "window_requests": u.RPM, "window_input_tokens": u.InputTPM} {
				ch <- prometheus.MustNewConstMetric(prometheus.NewDesc(prefix+name, "Recorded usage; estimated input and rolling 60-second windows.", labelNames, nil), prometheus.GaugeValue, float64(value), labels...)
			}
		}
	}
	emitResourceHealth(ch, scope, "usage", usageComplete)
}
func emitResourceHealth(ch chan<- prometheus.Metric, scope, component string, success bool) {
	value := 0.
	if success {
		value = 1
	}
	ch <- prometheus.MustNewConstMetric(prometheus.NewDesc("new_api_user_resource_scrape_success", "Whether resource configuration, discovery, usage or counter collection completed.", []string{"scope", "component"}, nil), prometheus.GaugeValue, value, scope, component)
}
func collectUserModelCounters(ctx context.Context, ch chan<- prometheus.Metric) bool {
	if ctx.Err() != nil {
		return false
	}
	userModelResourceEvents.Lock()
	now := time.Now()
	events := make(map[userModelResourceKey]userModelEvents, min(resourceDiscoveryLimit, len(userModelResourceEvents.events)))
	complete := true
	for key, event := range userModelResourceEvents.events {
		if ctx.Err() != nil {
			complete = false
			break
		}
		if !event.LastSeen.Add(userModelCounterIdle).After(now) {
			delete(userModelResourceEvents.events, key)
			continue
		}
		if len(events) >= resourceDiscoveryLimit {
			complete = false
			break
		}
		events[key] = userModelEvents{LastSeen: event.LastSeen, Admitted: event.Admitted, Rejected: maps.Clone(event.Rejected)}
	}
	userModelResourceEvents.Unlock()
	// Avoid emitting an arbitrary changing subset when cardinality exceeds the budget.
	if !complete {
		return false
	}
	for key, event := range events {
		if ctx.Err() != nil {
			return false
		}
		labels := []string{strconv.Itoa(key.UserID), key.Model}
		ch <- prometheus.MustNewConstMetric(prometheus.NewDesc("new_api_user_model_admitted_requests_total", "Process-local final joint admissions; resets after restart or 24h idle eviction.", []string{"user_id", "model"}, nil), prometheus.CounterValue, float64(event.Admitted), labels...)
		for _, reason := range append(append([]string{}, userResourceRejectionCodes[1:]...), "store_unavailable") {
			ch <- prometheus.MustNewConstMetric(prometheus.NewDesc("new_api_user_model_rejected_requests_total", "Process-local final joint rejections; resets after restart or 24h idle eviction.", []string{"user_id", "model", "reason"}, nil), prometheus.CounterValue, float64(event.Rejected[reason]), append(labels, reason)...)
		}
	}
	return true
}
