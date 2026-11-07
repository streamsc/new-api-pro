package service

import (
	"context"
	"strconv"
	"sync"

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
	config, err := setting.CurrentUserResourcesConfig()
	if err != nil || model.DB == nil {
		return
	}
	userResourceEvents.Lock()
	ids := make([]int, 0, len(userResourceEvents.ids))
	for id := range userResourceEvents.ids {
		ids = append(ids, id)
	}
	userResourceEvents.Unlock()
	scope := "memory"
	if common.RedisEnabled {
		scope = "redis"
		if common.RDB != nil {
			readCtx, cancel := context.WithTimeout(collector.Context, channelRedisTimeout)
			active, readErr := common.RDB.Eval(readCtx, `
local t=redis.call('TIME')
local now=t[1]*1000+math.floor(t[2]/1000)
redis.call('ZREMRANGEBYSCORE',KEYS[1],'-inf',now)
return redis.call('ZRANGE',KEYS[1],0,-1)
`, []string{userResourceKeys(1)[3]}).StringSlice()
			cancel()
			if readErr == nil {
				seen := map[int]bool{}
				for _, id := range ids {
					seen[id] = true
				}
				for _, raw := range active {
					id, parseErr := strconv.Atoi(raw)
					if parseErr == nil && !seen[id] {
						ids = append(ids, id)
						seen[id] = true
					}
				}
			}
		}
	}
	for offset := 0; offset < len(ids); offset += 100 {
		if collector.Context.Err() != nil {
			return
		}
		var users []struct {
			Id int
		}
		if err := model.DB.WithContext(collector.Context).Model(&model.User{}).Select("id").Where("id IN ?", ids[offset:min(offset+100, len(ids))]).Find(&users).Error; err != nil {
			return
		}
		for _, user := range users {
			labels := []string{scope, strconv.Itoa(user.Id)}
			limits := config.ForUser(user.Id)
			for resource, limit := range map[string]setting.ResourceLimit{"concurrency": limits.Concurrency, "input_tpm": limits.InputTPM, "rpm": limits.RPM} {
				enabled := 0.
				if limit.Enabled {
					enabled = 1
				}
				ch <- prometheus.MustNewConstMetric(prometheus.NewDesc("new_api_user_limit_enabled", "Whether the resource limit is enabled.", []string{"scope", "user_id", "resource"}, nil), prometheus.GaugeValue, enabled, append(labels, resource)...)
				ch <- prometheus.MustNewConstMetric(prometheus.NewDesc("new_api_user_limit", "Effective resource limit; interpret only when enabled.", []string{"scope", "user_id", "resource"}, nil), prometheus.GaugeValue, float64(limit.Limit), append(labels, resource)...)
			}
			usage, err := GetUserResourceUsage(collector.Context, user.Id)
			available := 0.
			if err == nil {
				available = 1
			}
			ch <- prometheus.MustNewConstMetric(prometheus.NewDesc("new_api_user_usage_available", "Whether usage was read successfully.", []string{"scope", "user_id"}, nil), prometheus.GaugeValue, available, labels...)
			if err != nil {
				continue
			}
			for name, value := range map[string]int{"inflight_requests": usage.Concurrency, "window_requests": usage.RPM, "window_input_tokens": usage.InputTPM} {
				ch <- prometheus.MustNewConstMetric(prometheus.NewDesc("new_api_user_"+name, "Current recorded usage; input tokens are estimates and windows are rolling 60 seconds.", []string{"scope", "user_id"}, nil), prometheus.GaugeValue, float64(value), labels...)
			}
		}
	}
}
