package controller

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/relaymetrics"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayCancellationAfterHeadersStopsRetry(t *testing.T) {
	previousMetrics := relaymetrics.Default
	relaymetrics.Default = relaymetrics.New()
	t.Cleanup(func() { relaymetrics.Default = previousMetrics })
	user, token := setupResponsesWSRequestTest(t)
	require.NoError(t, model.DB.Model(user).Update("setting", `{"billing_preference":"wallet_only"}`).Error)
	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		switch key {
		case "billing_setting.billing_mode", "billing_setting.billing_expr", "group_ratio_setting.group_ratio":
			saved[key] = value
		}
		return nil
	}))
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(saved)) })
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":    `{"cancel-test":"tiered_expr"}`,
		"billing_setting.billing_expr":    `{"cancel-test":"tier(\"request\", fixed(0))"}`,
		"group_ratio_setting.group_ratio": `{"default":1}`,
	}))
	previousDisable := common.AutomaticDisableChannelEnabled
	previousCodes := operation_setting.AutomaticDisableStatusCodeRanges
	common.AutomaticDisableChannelEnabled = true
	operation_setting.AutomaticDisableStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: 500, End: 500}}
	t.Cleanup(func() {
		common.AutomaticDisableChannelEnabled = previousDisable
		operation_setting.AutomaticDisableStatusCodeRanges = previousCodes
	})
	previousRetries := common.RetryTimes
	common.RetryTimes = 2
	t.Cleanup(func() { common.RetryTimes = previousRetries })
	service.InitHttpClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := service.GetHttpClient()
	transport := client.Transport
	bodyRead := make(chan struct{})
	client.Transport = &cancelBodyTransport{RoundTripper: transport, cancel: func() { close(bodyRead); cancel() }}
	t.Cleanup(func() { client.Transport = transport })
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Ability{}))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer upstream.Close()
	channel := &model.Channel{AutoBan: common.GetPointer(1), Name: "cancel-test", Key: "upstream-key", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeOpenAI, Group: "default", Models: "cancel-test", BaseURL: &upstream.URL}
	channel.SetSetting(dto.ChannelSettings{MaxConcurrency: 1})
	require.NoError(t, channel.Insert())
	t.Cleanup(func() { require.NoError(t, channel.Delete()) })
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"cancel-test","messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-"+token.Key)
	var policy *service.RequestPolicyState
	engine := gin.New()
	engine.POST("/v1/chat/completions", middleware.RequestId(), middleware.TokenAuth(), middleware.Distribute(), func(c *gin.Context) {
		Relay(c, types.RelayFormatOpenAI)
		policy = service.RequestPolicy(c)
	})
	done := make(chan struct{})
	recorder := httptest.NewRecorder()
	go func() { defer close(done); engine.ServeHTTP(recorder, req) }()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("relay did not stop after cancellation")
	}
	select {
	case <-bodyRead:
	default:
		t.Fatalf("response body not reached: %s", recorder.Body.String())
	}

	require.NotNil(t, policy)
	require.NotEmpty(t, policy.Events())
	assert.Equal(t, 1, policy.Attempts)
	for _, event := range policy.Events() {
		assert.NotEqual(t, "retry", event.Decision.Action)
		assert.NotEqual(t, "failure", event.Decision.Action, "client cancellation must not be recorded as a channel failure")
	}
	events := policy.Events()
	assert.Equal(t, "stop", events[len(events)-1].Decision.Action)
	assert.Equal(t, "unchanged", events[len(events)-1].Health)
	var savedChannel model.Channel
	require.NoError(t, model.DB.First(&savedChannel, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, savedChannel.Status)
	counts, err := service.GetChannelConcurrencyCounts(context.Background(), []int{channel.Id})
	require.NoError(t, err)
	assert.Zero(t, counts[channel.Id], "canceled relay must release its lease")
	resourceUsage, resourceErr := service.GetUserResourceUsage(context.Background(), user.Id)
	require.NoError(t, resourceErr)
	assert.Zero(t, resourceUsage.Concurrency)
	assert.Equal(t, 1, resourceUsage.RPM)
	assert.Positive(t, resourceUsage.InputTPM)
	registry := prometheus.NewRegistry()
	require.NoError(t, registry.Register(relaymetrics.Default))
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		require.Len(t, family.Metric, 1)
		metric := family.Metric[0]
		assert.Equal(t, "cancel-test", metric.Label[0].GetValue())
		assert.Equal(t, "cancelled", metric.Label[1].GetValue())
		if metric.Counter != nil {
			assert.Equal(t, 1.0, metric.Counter.GetValue())
		}
		if metric.Histogram != nil {
			assert.EqualValues(t, 1, metric.Histogram.GetSampleCount())
		}
	}
}

// Cancel only when the response handler starts reading, after Client.Do returned.
type cancelBodyTransport struct {
	http.RoundTripper
	cancel context.CancelFunc
}

func (t *cancelBodyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.RoundTripper.RoundTrip(req)
	if err == nil {
		resp.Body = &cancelOnReadBody{ReadCloser: resp.Body, cancel: t.cancel}
	}
	return resp, err
}

type cancelOnReadBody struct {
	io.ReadCloser
	once   sync.Once
	cancel context.CancelFunc
}

func (b *cancelOnReadBody) Read(p []byte) (int, error) {
	b.once.Do(b.cancel)
	return b.ReadCloser.Read(p)
}

func TestRelayPrometheusBoundary(t *testing.T) {
	user, token := setupResponsesWSRequestTest(t)
	common.OptionMapRWMutex.Lock()
	previousOptions := common.OptionMap
	common.OptionMap = map[string]string{}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptions
		common.OptionMapRWMutex.Unlock()
	})
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Ability{}))
	require.NoError(t, model.DB.Model(user).Update("setting", `{"billing_preference":"wallet_only"}`).Error)
	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		switch key {
		case "billing_setting.billing_mode", "billing_setting.billing_expr", "group_ratio_setting.group_ratio", "perf_metrics_setting.enabled":
			saved[key] = value
		}
		return nil
	}))
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(saved)) })
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"perf_metrics_setting.enabled":    "false",
		"billing_setting.billing_mode":    `{"metrics-test":"tiered_expr"}`,
		"billing_setting.billing_expr":    `{"metrics-test":"tier(\"request\", fixed(0))"}`,
		"group_ratio_setting.group_ratio": `{"default":1}`,
	}))
	previousRetries, previousDisable := common.RetryTimes, common.AutomaticDisableChannelEnabled
	common.RetryTimes, common.AutomaticDisableChannelEnabled = 1, false
	t.Cleanup(func() { common.RetryTimes, common.AutomaticDisableChannelEnabled = previousRetries, previousDisable })
	previousTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 10
	t.Cleanup(func() { constant.StreamingTimeout = previousTimeout })
	service.InitHttpClient()
	for _, tc := range []struct {
		name, result                                                                   string
		stream, retry, blocked, storeFailure, userLimit, badBody, legacy, cancelStream bool
		upstreamStatus                                                                 int
		body                                                                           string
		calls                                                                          int
	}{
		{name: "sync success", result: "success", calls: 1},
		{name: "retry success", result: "success", retry: true, calls: 2},
		{name: "sync failure", result: "failure", upstreamStatus: 400, calls: 1},
		{name: "upstream 429", result: "failure", upstreamStatus: 429, calls: 2},
		{name: "slot rejected", result: "failure", blocked: true},
		{name: "redis error", result: "failure", storeFailure: true},
		{name: "user limit", result: "failure", userLimit: true},
		{name: "early validation", result: "failure", badBody: true},
		{name: "legacy stream unknown", result: "unknown", stream: true, legacy: true, body: "{\"model\":\"mapped\",\"message\":{\"role\":\"assistant\",\"content\":\"ok\"},\"done\":true}\n", calls: 1},
		{name: "stream cancelled", result: "cancelled", stream: true, cancelStream: true, body: "data: {}\n\n", calls: 1},
		{name: "stream success", result: "success", stream: true, body: "data: {\"id\":\"test\",\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", calls: 1},
		{name: "stream failure after 200", result: "failure", stream: true, body: "data: {\"error\":{\"message\":\"failed\",\"type\":\"server_error\",\"code\":\"server_error\"}}\n\n", calls: 1},
		{name: "truncated stream", result: "failure", stream: true, body: "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n", calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previousMetrics := relaymetrics.Default
			relaymetrics.Default = relaymetrics.New()
			t.Cleanup(func() { relaymetrics.Default = previousMetrics })
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if tc.retry && n == 1 {
					w.WriteHeader(500)
					_, _ = io.WriteString(w, `{"error":{"message":"retry","type":"server_error"}}`)
					return
				}
				if tc.upstreamStatus != 0 {
					w.WriteHeader(tc.upstreamStatus)
					_, _ = io.WriteString(w, `{"error":{"message":"failed","type":"server_error"}}`)
					return
				}
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, tc.body)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"test","model":"mapped","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
			}))
			defer upstream.Close()
			mapping := `{"metrics-test":"mapped"}`
			ch := &model.Channel{Name: "metrics", Key: "key", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeOpenAI, Group: "default", Models: "metrics-test", BaseURL: &upstream.URL, ModelMapping: &mapping}
			if tc.legacy {
				ch.Type = constant.ChannelTypeOllama
			}
			ch.SetSetting(dto.ChannelSettings{MaxConcurrency: 1})
			require.NoError(t, ch.Insert())
			t.Cleanup(func() { require.NoError(t, ch.Delete()) })
			if tc.blocked {
				lease, _, err := service.AcquireChannelConcurrency(context.Background(), ch.Id, 1, "occupied")
				require.NoError(t, err)
				defer lease.Release()
			}
			if tc.userLimit {
				common.OptionMapRWMutex.Lock()
				previous := common.OptionMap[setting.UserResourcesOption]
				common.OptionMap[setting.UserResourcesOption] = `{"defaults":{"rpm":{"enabled":true,"limit":0}}}`
				common.OptionMapRWMutex.Unlock()
				defer func() {
					common.OptionMapRWMutex.Lock()
					common.OptionMap[setting.UserResourcesOption] = previous
					common.OptionMapRWMutex.Unlock()
				}()
			}
			previousRedis, previousRDB := common.RedisEnabled, common.RDB
			defer func() { common.RedisEnabled, common.RDB = previousRedis, previousRDB }()
			engine := gin.New()
			engine.POST("/v1/chat/completions", middleware.RequestId(), middleware.TokenAuth(), middleware.Distribute(), func(c *gin.Context) {
				if tc.storeFailure {
					common.RedisEnabled = true
					common.RDB = redis.NewClient(&redis.Options{Addr: "127.0.0.1:33516"})
					require.NoError(t, common.RDB.Close())
				}
				Relay(c, types.RelayFormatOpenAI)
			})
			body := fmt.Sprintf(`{"model":"metrics-test","stream":%t,"messages":[{"role":"user","content":"hi"}]}`, tc.stream)
			if tc.badBody {
				body = `{"model":"metrics-test","messages":[]}`
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			requestContext, cancel := context.WithCancel(context.Background())
			defer cancel()
			req = req.WithContext(requestContext)
			if tc.cancelStream {
				client := service.GetHttpClient()
				previousTransport := client.Transport
				client.Transport = &cancelBodyTransport{RoundTripper: previousTransport, cancel: cancel}
				defer func() { client.Transport = previousTransport }()
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer sk-"+token.Key)
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, req)
			assert.EqualValues(t, tc.calls, calls.Load(), recorder.Body.String())
			if tc.stream && !tc.cancelStream {
				assert.Equal(t, 200, recorder.Code)
			}
			registry := prometheus.NewPedanticRegistry()
			require.NoError(t, registry.Register(relaymetrics.Default))
			families, err := registry.Gather()
			require.NoError(t, err)
			observed, rejected := 0, 0
			for _, family := range families {
				require.Len(t, family.Metric, 1)
				metric := family.Metric[0]
				if family.GetName() == "new_api_channel_concurrency_rejected_total" {
					rejected++
					assert.Equal(t, 1.0, metric.Counter.GetValue())
					assert.Equal(t, strconv.Itoa(ch.Id), metric.Label[0].GetValue())
					assert.Equal(t, "metrics-test", metric.Label[1].GetValue())
					continue
				}
				observed++
				assert.Equal(t, "metrics-test", metric.Label[0].GetValue())
				assert.Equal(t, tc.result, metric.Label[1].GetValue())
				if metric.Counter != nil {
					assert.Equal(t, 1.0, metric.Counter.GetValue(), "retries must not duplicate logical requests")
				}
				if metric.Histogram != nil {
					assert.EqualValues(t, 1, metric.Histogram.GetSampleCount())
					assert.Positive(t, metric.Histogram.GetSampleSum())
				}
			}
			assert.Equal(t, 2, observed)
			if tc.blocked {
				assert.Equal(t, 1, rejected)
			} else {
				assert.Zero(t, rejected)
			}
		})
	}
}
