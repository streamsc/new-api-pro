package channel

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	common2 "github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestProcessHeaderOverride_ChannelTestSkipsPassthroughRules(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"*": "",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Empty(t, headers)
}

func TestProcessHeaderOverride_ChannelTestSkipsClientHeaderPlaceholder(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Upstream-Trace": "{client_header:X-Trace-Id}",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	_, ok := headers["x-upstream-trace"]
	require.False(t, ok)
}

func TestProcessHeaderOverride_NonTestKeepsClientHeaderPlaceholder(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Upstream-Trace": "{client_header:X-Trace-Id}",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "trace-123", headers["x-upstream-trace"])
}

func TestProcessHeaderOverride_RuntimeOverrideIsFinalHeaderMap(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		IsChannelTest:             false,
		UseRuntimeHeadersOverride: true,
		RuntimeHeadersOverride: map[string]any{
			"x-static":  "runtime-value",
			"x-runtime": "runtime-only",
		},
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Static": "legacy-value",
				"X-Legacy": "legacy-only",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "runtime-value", headers["x-static"])
	require.Equal(t, "runtime-only", headers["x-runtime"])
	_, exists := headers["x-legacy"]
	require.False(t, exists)
}

func TestProcessHeaderOverride_PassthroughSkipsAcceptEncoding(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")
	ctx.Request.Header.Set("Accept-Encoding", "gzip")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"*": "",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "trace-123", headers["x-trace-id"])

	_, hasAcceptEncoding := headers["accept-encoding"]
	require.False(t, hasAcceptEncoding)
}

func TestProcessHeaderOverride_PassHeadersTemplateSetsRuntimeHeaders(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx.Request.Header.Set("Originator", "Codex CLI")
	ctx.Request.Header.Set("Session_id", "sess-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		RequestHeaders: map[string]string{
			"Originator": "Codex CLI",
			"Session_id": "sess-123",
		},
		ChannelMeta: &relaycommon.ChannelMeta{
			ParamOverride: map[string]any{
				"operations": []any{
					map[string]any{
						"mode":  "pass_headers",
						"value": []any{"Originator", "Session_id", "X-Codex-Beta-Features"},
					},
				},
			},
			HeadersOverride: map[string]any{
				"X-Static": "legacy-value",
			},
		},
	}

	_, err := relaycommon.ApplyParamOverrideWithRelayInfo([]byte(`{"model":"gpt-4.1"}`), info)
	require.NoError(t, err)
	require.True(t, info.UseRuntimeHeadersOverride)
	require.Equal(t, "Codex CLI", info.RuntimeHeadersOverride["originator"])
	require.Equal(t, "sess-123", info.RuntimeHeadersOverride["session_id"])
	_, exists := info.RuntimeHeadersOverride["x-codex-beta-features"]
	require.False(t, exists)
	require.Equal(t, "legacy-value", info.RuntimeHeadersOverride["x-static"])

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "Codex CLI", headers["originator"])
	require.Equal(t, "sess-123", headers["session_id"])
	_, exists = headers["x-codex-beta-features"]
	require.False(t, exists)

	upstreamReq := httptest.NewRequest(http.MethodPost, "https://example.com/v1/responses", nil)
	applyHeaderOverrideToRequest(upstreamReq, headers)
	require.Equal(t, "Codex CLI", upstreamReq.Header.Get("Originator"))
	require.Equal(t, "sess-123", upstreamReq.Header.Get("Session_id"))
	require.Empty(t, upstreamReq.Header.Get("X-Codex-Beta-Features"))
}

func TestProcessHeaderOverride_ContextHMACUsesAuthenticatedIdentity(t *testing.T) {
	originalSecret := common2.CryptoSecret
	common2.CryptoSecret = "context-hmac-test-secret"
	t.Cleanup(func() { common2.CryptoSecret = originalSecret })

	tests := []struct {
		name      string
		path      string
		template  string
		source    string
		id        int
		isStream  bool
		relayMode int
	}{
		{
			name:     "authenticated user on regular relay",
			path:     "/v1/chat/completions",
			template: contextHMACUserIDPlaceholder,
			source:   "user_id",
			id:       101,
		},
		{
			name:     "authenticated token on streaming relay",
			path:     "/v1/responses",
			template: "  " + contextHMACTokenIDPlaceholder + "  ",
			source:   "token_id",
			id:       202,
			isStream: true,
		},
		{
			name:      "authenticated token on realtime relay",
			path:      "/v1/realtime",
			template:  contextHMACTokenIDPlaceholder,
			source:    "token_id",
			id:        202,
			relayMode: constant.RelayModeRealtime,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, tt.path, nil)
			ctx.Request.Header.Set("X-Affinity-Key", "client-forged-value")

			info := &relaycommon.RelayInfo{
				UserId:    101,
				TokenId:   202,
				IsStream:  tt.isStream,
				RelayMode: tt.relayMode,
				ChannelMeta: &relaycommon.ChannelMeta{
					HeadersOverride: map[string]any{
						"*":              true,
						"X-Affinity-Key": tt.template,
					},
				},
			}

			headers, err := processHeaderOverride(info, ctx)
			require.NoError(t, err)
			expectedInput := "new-api-pro:context-hmac:v1:" + tt.source + ":" + fmt.Sprint(tt.id)
			expected := "v1:" + tt.source + ":" + common2.GenerateHMAC(expectedInput)
			require.Equal(t, expected, headers["x-affinity-key"])
			require.Regexp(t, "^v1:"+tt.source+":[0-9a-f]{64}$", headers["x-affinity-key"])

			repeated, err := processHeaderOverride(info, ctx)
			require.NoError(t, err)
			require.Equal(t, headers["x-affinity-key"], repeated["x-affinity-key"])
		})
	}
}

func TestProcessHeaderOverride_ContextHMACSeparatesSourceIDAndSecret(t *testing.T) {
	originalSecret := common2.CryptoSecret
	t.Cleanup(func() { common2.CryptoSecret = originalSecret })

	resolve := func(secret, template string, userID, tokenID int) string {
		common2.CryptoSecret = secret
		info := &relaycommon.RelayInfo{
			UserId:  userID,
			TokenId: tokenID,
			ChannelMeta: &relaycommon.ChannelMeta{
				HeadersOverride: map[string]any{"X-Affinity-Key": template},
			},
		}
		headers, err := processHeaderOverride(info, nil)
		require.NoError(t, err)
		return headers["x-affinity-key"]
	}

	userOne := resolve("secret-a", contextHMACUserIDPlaceholder, 7, 7)
	tokenOne := resolve("secret-a", contextHMACTokenIDPlaceholder, 7, 7)
	userTwo := resolve("secret-a", contextHMACUserIDPlaceholder, 8, 7)
	rotatedSecret := resolve("secret-b", contextHMACUserIDPlaceholder, 7, 7)

	require.NotEqual(t, userOne, tokenOne)
	require.NotEqual(t, userOne, userTwo)
	require.NotEqual(t, userOne, rotatedSecret)
}

func TestProcessHeaderOverride_ContextHMACOmitsMissingIdentityAndChannelTests(t *testing.T) {
	tests := []struct {
		name          string
		template      string
		userID        int
		tokenID       int
		isChannelTest bool
	}{
		{name: "missing user ID", template: contextHMACUserIDPlaceholder, tokenID: 22},
		{name: "negative user ID", template: contextHMACUserIDPlaceholder, userID: -1, tokenID: 22},
		{name: "missing token ID", template: contextHMACTokenIDPlaceholder, userID: 11},
		{name: "negative token ID", template: contextHMACTokenIDPlaceholder, userID: 11, tokenID: -1},
		{name: "channel test user", template: contextHMACUserIDPlaceholder, userID: 11, tokenID: 22, isChannelTest: true},
		{name: "channel test token", template: contextHMACTokenIDPlaceholder, userID: 11, tokenID: 22, isChannelTest: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{
				UserId:        tt.userID,
				TokenId:       tt.tokenID,
				IsChannelTest: tt.isChannelTest,
				ChannelMeta: &relaycommon.ChannelMeta{
					HeadersOverride: map[string]any{"X-Affinity-Key": tt.template},
				},
			}

			headers, err := processHeaderOverride(info, nil)
			require.NoError(t, err)
			require.NotContains(t, headers, "x-affinity-key")
		})
	}
}

func TestProcessHeaderOverride_ContextHMACRejectsInvalidPlaceholders(t *testing.T) {
	templates := []string{
		"{context_hmac:unknown}",
		"prefix-{context_hmac:user_id}",
		"{context_hmac:user_id}-suffix",
		"{CONTEXT_HMAC:user_id}",
		"{context_hmac:user_id",
		"{context_hmacuser_id}",
	}

	for _, template := range templates {
		t.Run(template, func(t *testing.T) {
			info := &relaycommon.RelayInfo{
				UserId:        424242,
				TokenId:       525252,
				IsChannelTest: true,
				ChannelMeta: &relaycommon.ChannelMeta{
					HeadersOverride: map[string]any{"X-Affinity-Key": template},
				},
			}

			_, err := processHeaderOverride(info, nil)
			require.Error(t, err)
			var apiErr *types.NewAPIError
			require.ErrorAs(t, err, &apiErr)
			require.Equal(t, types.ErrorCodeChannelHeaderOverrideInvalid, apiErr.GetErrorCode())
			require.NotContains(t, err.Error(), "424242")
			require.NotContains(t, err.Error(), "525252")
		})
	}
}

func TestProcessHeaderOverride_ContextHMACPreservesExistingValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Client", "client-value")

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ApiKey: "channel-secret",
			HeadersOverride: map[string]any{
				"Authorization": "Bearer {api_key}",
				"X-Client":      "{client_header:X-Client}",
				"X-Static":      "static-value",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "Bearer channel-secret", headers["authorization"])
	require.Equal(t, "client-value", headers["x-client"])
	require.Equal(t, "static-value", headers["x-static"])
}
