package router

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAffinityDiagnosticAdminAPI(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	oldDB, oldLog, oldRedis, oldSecret, oldType := model.DB, model.LOG_DB, common.RedisEnabled, common.SessionSecret, common.MainDatabaseType()
	model.DB = db
	model.LOG_DB = db
	common.RedisEnabled = false
	common.SessionSecret = "diagnostic-route-test"
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.DB = oldDB
		model.LOG_DB = oldLog
		common.RedisEnabled = oldRedis
		common.SessionSecret = oldSecret
		common.SetMainDatabaseType(oldType)
		sqlDB.Close()
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.AuditLog{}, &model.UserAccessToken{}, &model.Option{}, &model.AffinityDiagnosticControl{}, &model.AffinityDiagnosticTask{}, &model.AffinityDiagnosticSample{}))
	require.NoError(t, model.InitAffinityDiagnostics(context.Background(), true))
	tokens := map[int]string{}
	for i, role := range []int{common.RoleCommonUser, common.RoleAdminUser} {
		user := model.User{Id: 120 + i, Username: fmt.Sprintf("diagnostic-%d", role), Role: role, Status: common.UserStatusEnabled, AuthVersion: 1, AffCode: fmt.Sprintf("diagnostic-%d", role)}
		require.NoError(t, db.Create(&user).Error)
		session := &model.UserSession{SID: fmt.Sprintf("diagnostic-%d", role), UserID: user.Id, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, RefreshHash: fmt.Sprintf("hash-%d", role), LoginMethod: "password", LastActiveAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()}
		require.NoError(t, model.CreateUserSession(session))
		token, _, err := service.IssueAccessToken(service.AuthIdentity{UserID: user.Id, SessionID: session.SID, UserAuthVersion: 1, SessionVersion: 1})
		require.NoError(t, err)
		tokens[role] = token
	}
	engine := gin.New()
	registerAffinityDiagnosticRoutes(engine.Group("/api"))
	task := model.AffinityDiagnosticTask{UserID: 120, AdminID: 121, StartedAt: time.Now().UnixMilli(), ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), Status: "running", MaxCount: 100, MaxBytes: 20 << 20, ReservedBytes: 20 << 20}
	require.NoError(t, model.CreateAffinityDiagnostic(context.Background(), &task))
	raw := []byte(" { \"secret-in-body\": \"preserved\", \"n\":1e2 }\n")
	metadata, err := common.Marshal(service.DiagnosticMetadata{BodyStatus: "captured", BodyKind: "http_json", Headers: http.Header{"X-Session": []string{"first", "second"}}})
	require.NoError(t, err)
	sample := model.AffinityDiagnosticSample{TaskID: task.ID, Body: raw, Metadata: metadata, BodyStatus: "captured", SizeBytes: int64(len(raw) + len(metadata))}
	require.NoError(t, model.SaveAffinityDiagnosticSample(context.Background(), &sample))
	base := fmt.Sprintf("/api/affinity-diagnostics/%d/samples/%d", task.ID, sample.ID)
	request := func(method, path, token string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/affinity-diagnostics", fmt.Sprintf("/api/affinity-diagnostics/%d/samples", task.ID), base, base + "?download=body"} {
		assert.Equal(t, 401, request("GET", path, "", nil).Code)
		denied := request("GET", path, tokens[common.RoleCommonUser], nil)
		assert.Equal(t, 403, denied.Code)
		assert.NotContains(t, denied.Body.String(), "preserved")
	}
	for _, route := range []struct{ method, path string }{{"POST", "/api/affinity-diagnostics"}, {"POST", fmt.Sprintf("/api/affinity-diagnostics/%d/stop", task.ID)}, {"DELETE", fmt.Sprintf("/api/affinity-diagnostics/%d", task.ID)}, {"POST", base + "/extract"}} {
		assert.Equal(t, 403, request(route.method, route.path, tokens[common.RoleCommonUser], []byte(`{}`)).Code)
	}
	// Raw request contents are not covered by ordinary log/channel PAT scopes.
	rawPAT := model.AccessTokenPrefix + strings.Repeat("a", 43)
	pat := &model.UserAccessToken{Name: "diagnostic-denied", TokenHash: model.AccessTokenFingerprint(rawPAT), TokenHint: model.AccessTokenHint(rawPAT), ExpiresAt: time.Now().Add(time.Hour).Unix()}
	require.NoError(t, pat.SetScopes([]string{"log:read", "channel:read", "channel:write"}))
	require.NoError(t, model.CreateUserAccessToken(121, pat, service.AccessTokenMaxPerUser))
	assert.Equal(t, 403, request("GET", base+"?download=body", rawPAT, nil).Code)
	assert.Equal(t, 403, request("POST", "/api/affinity-diagnostics", rawPAT, []byte(`{}`)).Code)
	admin := tokens[common.RoleAdminUser]
	download := request("GET", base+"?download=body", admin, nil)
	require.Equal(t, 200, download.Code, download.Body.String())
	assert.Equal(t, raw, download.Body.Bytes())
	assert.Equal(t, "no-store", download.Header().Get("Cache-Control"))
	assert.Contains(t, download.Header().Get("Content-Disposition"), "attachment")
	extraction := request("POST", base+"/extract", admin, []byte(`{"type":"gjson","path":"n"}`))
	require.Equal(t, 200, extraction.Code, extraction.Body.String())
	assert.Contains(t, extraction.Body.String(), `"value":"100"`)
	var audits []model.AuditLog
	require.NoError(t, db.Find(&audits).Error)
	encoded, err := common.Marshal(audits)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "preserved")
	require.NoError(t, db.Model(&task).Update("expires_at", time.Now().Add(-time.Second).UnixMilli()).Error)
	assert.Equal(t, 410, request("GET", base+"?download=body", admin, nil).Code)
	assert.Equal(t, 410, request("POST", base+"/extract", admin, []byte(`{"type":"gjson","path":"n"}`)).Code)
}
