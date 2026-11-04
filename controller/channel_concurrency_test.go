package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/alicebob/miniredis/v2"
	redisserver "github.com/alicebob/miniredis/v2/server"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestChannelConcurrencyReadResponses(t *testing.T) {
	for _, mode := range []string{"process", "redis", "unavailable"} {
		for _, empty := range []bool{false, true} {
			t.Run(mode+map[bool]string{true: "/empty", false: "/channels"}[empty], func(t *testing.T) {
				previousRedis, previousClient := common.RedisEnabled, common.RDB
				t.Cleanup(func() { common.RedisEnabled, common.RDB = previousRedis, previousClient })
				db := setupModelListControllerTestDB(t)
				if mode != "process" {
					server := miniredis.RunT(t)
					client := redis.NewClient(&redis.Options{Addr: server.Addr()})
					t.Cleanup(func() { _ = client.Close() })
					common.RedisEnabled, common.RDB = true, client
				}
				setting := `{"max_concurrency":10}`
				if !empty {
					require.NoError(t, db.Create(&model.Channel{Id: 301, Name: "counted", Status: 1, Setting: &setting}).Error)
					require.NoError(t, db.Create(&model.Channel{Id: 302, Name: "unlimited", Status: 1}).Error)
					lease, _, err := service.AcquireChannelConcurrency(context.Background(), 301, 10, "observed")
					require.NoError(t, err)
					t.Cleanup(lease.Release)
				}
				if mode == "unavailable" {
					require.NoError(t, common.RDB.Close())
					if empty {
						common.RDB = nil
					}
				}
				router := gin.New()
				router.GET("/channel", GetAllChannels)
				router.GET("/search", SearchChannels)
				router.GET("/channel/:id", GetChannel)
				paths := []string{"/channel", "/search?keyword="}
				if !empty {
					paths = append(paths, "/channel/301")
				}
				for _, path := range paths {
					recorder := httptest.NewRecorder()
					router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
					require.Equal(t, http.StatusOK, recorder.Code, path)
					var response struct {
						Success bool           `json:"success"`
						Data    map[string]any `json:"data"`
					}
					require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
					require.True(t, response.Success, recorder.Body.String())
					if path == "/channel/301" {
						require.Equal(t, setting, response.Data["setting"])
						if mode == "unavailable" {
							require.Contains(t, response.Data, "in_flight")
							require.Nil(t, response.Data["in_flight"])
						} else {
							require.EqualValues(t, 1, response.Data["in_flight"])
						}
						continue
					}
					scope := "redis"
					if mode == "process" {
						scope = "process"
					}
					require.Equal(t, scope, response.Data["in_flight_scope"])
					require.Equal(t, mode != "unavailable", response.Data["in_flight_available"])
					items := response.Data["items"].([]any)
					if empty {
						require.Empty(t, items)
						continue
					}
					require.Len(t, items, 2)
					for _, raw := range items {
						item := raw.(map[string]any)
						require.Contains(t, item, "in_flight")
						if mode == "unavailable" {
							require.Nil(t, item["in_flight"])
							continue
						}
						count := 0
						if item["id"] == float64(301) {
							count = 1
						}
						require.EqualValues(t, count, item["in_flight"])
					}
				}
			})
		}
	}
}

func TestChannelConcurrencyReadFailureDoesNotReleaseActiveRequest(t *testing.T) {
	previousRedis, previousClient := common.RedisEnabled, common.RDB
	t.Cleanup(func() { common.RedisEnabled, common.RDB = previousRedis, previousClient })
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.Create(&model.Channel{Id: 401, Name: "active", Status: 1}).Error)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	common.RedisEnabled, common.RDB = true, client
	lease, _, err := service.AcquireChannelConcurrency(context.Background(), 401, 10, "active")
	require.NoError(t, err)
	t.Cleanup(lease.Release)
	server.Server().SetPreHook(func(peer *redisserver.Peer, command string, args ...string) bool {
		if command == "EVAL" && len(args) > 0 && strings.Contains(args[0], "ZREMRANGEBYSCORE") && !strings.Contains(args[0], "ZADD") {
			peer.WriteError("ERR injected display read failure")
			return true
		}
		return false
	})
	router := gin.New()
	router.GET("/channel", GetAllChannels)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/channel", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"in_flight_available":false`)
	require.NoError(t, lease.Context().Err())
	server.Server().SetPreHook(nil)
	counts, err := service.GetChannelConcurrencyCounts(context.Background(), []int{401})
	require.NoError(t, err)
	require.Equal(t, 1, counts[401])
	var channel model.Channel
	require.NoError(t, db.First(&channel, 401).Error)
	require.Equal(t, 1, channel.Status)
	lease.Release()
	counts, err = service.GetChannelConcurrencyCounts(context.Background(), []int{401})
	require.NoError(t, err)
	require.Zero(t, counts[401])
}

func TestUserResourceSelfReadIgnoresRequestedIdentity(t *testing.T) {
	user, _ := setupResponsesWSRequestTest(t)
	previousMap := common.OptionMap
	common.OptionMap = map[string]string{}
	defer func() { common.OptionMap = previousMap }()
	release, rejected, err := service.AcquireUserResources(context.Background(), user.Id, 19, setting.UserResourceLimits{})
	require.NoError(t, err)
	require.Nil(t, rejected)
	defer release()
	engine := gin.New()
	engine.GET("/self/resources", func(c *gin.Context) { c.Set("id", user.Id); GetSelfResources(c) })
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest("GET", "/self/resources?user_id=999999", nil))
	require.Equal(t, 200, recorder.Code)
	var response struct {
		Success bool
		Data    struct {
			UserID    int `json:"user_id"`
			Usage     service.UserResourceUsage
			Available bool
		}
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.True(t, response.Data.Available)
	require.Equal(t, user.Id, response.Data.UserID)
	require.Equal(t, 19, response.Data.Usage.InputTPM)
	common.RedisEnabled = true
	common.RDB = nil
	recorder = httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest("GET", "/self/resources", nil))
	require.Contains(t, recorder.Body.String(), `"available":false`)
	require.NotContains(t, recorder.Body.String(), `"usage"`)
	common.RedisEnabled = false
}

func TestUserResourcesAdmissionIndependentOfBillingAndKey(t *testing.T) {
	user, _ := setupResponsesWSRequestTest(t)
	previousMap := common.OptionMap
	common.OptionMap = map[string]string{setting.UserResourcesOption: `{"defaults":{"rpm":{"enabled":true,"limit":1}}}`}
	defer func() { common.OptionMap = previousMap }()
	previousCount := constant.CountToken
	constant.CountToken = false
	defer func() { constant.CountToken = previousCount }()
	request := &dto.GeneralOpenAIRequest{Model: "gpt-4o", Messages: []dto.Message{{Role: "user", Content: "hello world"}}}
	info := &relaycommon.RelayInfo{UserId: user.Id, OriginModelName: "gpt-4o", RelayFormat: types.RelayFormatOpenAI, Request: request}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	release, apiErr := relay.AdmitUserResources(c, info)
	require.Nil(t, apiErr)
	require.NotNil(t, release)
	usage, err := service.GetUserResourceUsage(context.Background(), user.Id)
	require.NoError(t, err)
	require.Positive(t, usage.InputTPM)
	require.Equal(t, 1, usage.RPM)
	release()
	release()
	info.TokenId = 9876
	_, apiErr = relay.AdmitUserResources(c, info)
	require.NotNil(t, apiErr)
	require.Equal(t, 429, apiErr.StatusCode)
	require.Equal(t, types.ErrorCode("user_rpm_exceeded"), apiErr.GetErrorCode())
	require.NotEmpty(t, recorder.Header().Get("Retry-After"))
	usage, err = service.GetUserResourceUsage(context.Background(), user.Id)
	require.NoError(t, err)
	require.Equal(t, 1, usage.RPM)
	require.Zero(t, usage.Concurrency)
}

func TestUserResourceMetricsRenameAndUnavailableUsage(t *testing.T) {
	user, _ := setupResponsesWSRequestTest(t)
	previousMap := common.OptionMap
	common.OptionMap = map[string]string{}
	defer func() { common.OptionMap = previousMap }()
	release, rejection, err := service.AcquireUserResources(context.Background(), user.Id, 23, setting.UserResourceLimits{})
	require.NoError(t, err)
	require.Nil(t, rejection)
	defer release()
	registry := prometheus.NewRegistry()
	registry.MustRegister(service.UserResourcesCollector{Context: context.Background()})
	require.Equal(t, 1, testutil.CollectAndCount(service.UserResourcesCollector{Context: context.Background()}, "new_api_user_inflight_requests"))
	require.NoError(t, model.DB.Model(user).Update("username", "renamed-resource-user").Error)
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() == "new_api_user_info" {
			require.Len(t, family.Metric, 1)
			for _, label := range family.Metric[0].Label {
				if label.GetName() == "username" {
					require.Equal(t, "renamed-resource-user", label.GetValue())
				}
			}
		}
	}
	common.RedisEnabled = true
	common.RDB = nil
	require.Zero(t, testutil.CollectAndCount(service.UserResourcesCollector{Context: context.Background()}, "new_api_user_inflight_requests"))
	require.Equal(t, 3, testutil.CollectAndCount(service.UserResourcesCollector{Context: context.Background()}, "new_api_user_limit_enabled"))
	common.RedisEnabled = false
}
