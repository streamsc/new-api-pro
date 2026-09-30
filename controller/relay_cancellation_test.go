package controller

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayCancellationAfterHeadersStopsRetry(t *testing.T) {
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
