package controller

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
)

func GetSelfResources(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	id := c.GetInt("id")
	user, err := model.GetUserById(id, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	config, err := setting.CurrentUserResourcesConfig()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	usage, err := service.GetUserResourceUsage(ctx, id)
	mode := "memory"
	if common.RedisEnabled {
		mode = "redis"
	}
	page, pageSize := resourcePage(c)
	rows, total, complete := service.GetUserModelResourcePage(ctx, config, id, c.Query("model"), page, pageSize)
	data := gin.H{"models": rows, "models_total": total, "models_complete": complete, "page": page, "page_size": pageSize, "sources": setting.ResourceSources(config.Users[strconv.Itoa(id)], "user_default"), "user_id": id, "username": user.Username, "limits": config.ForUser(id), "mode": mode, "available": err == nil, "updated_at": time.Now().Unix()}
	if err == nil {
		data["usage"] = usage
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}
func resourcePage(c *gin.Context) (int, int) {
	page, _ := strconv.Atoi(c.DefaultQuery("p", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	return min(max(page, 1), 1000000), min(max(size, 1), 100)
}

// Read-only admin inventory. Self queries use the authenticated identity above,
// never a caller-supplied user_id. Writes remain root-only with an audit reason.
func GetAdminResources(c *gin.Context) {
	id := 0
	if raw := c.Query("user_id"); raw != "" {
		var err error
		id, err = strconv.Atoi(raw)
		if err != nil || id <= 0 {
			common.ApiError(c, errors.New("Invalid user ID"))
			return
		}
	}
	config, err := setting.CurrentUserResourcesConfig()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	page, size := resourcePage(c)
	rows, total, complete := service.GetUserModelResourcePage(ctx, config, id, c.Query("model"), page, size)
	mode := "memory"
	if common.RedisEnabled {
		mode = "redis"
	}
	data := gin.H{"items": rows, "total": total, "complete": complete, "page": page, "page_size": size, "mode": mode, "updated_at": time.Now().Unix()}
	if id > 0 {
		usage, readErr := service.GetUserResourceUsage(ctx, id)
		resource := gin.H{"limits": config.ForUser(id), "sources": setting.ResourceSources(config.Users[strconv.Itoa(id)], "user_default"), "available": readErr == nil}
		if readErr == nil {
			resource["usage"] = usage
		}
		data["user_total"] = resource
	}
	common.ApiSuccess(c, data)
}
func GetUserResourcesConfig(c *gin.Context) {
	config, err := setting.CurrentUserResourcesConfig()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": config, "version": setting.UserResourcesVersion(config)})
}
func UpdateUserResourcesConfig(c *gin.Context) {
	var request struct {
		ExpectedVersion string                      `json:"expected_version"`
		Config          setting.UserResourcesConfig `json:"config"`
		Reason          string                      `json:"reason"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	if strings.TrimSpace(request.Reason) == "" || utf8.RuneCountInString(request.Reason) > 1000 {
		common.ApiError(c, errors.New("A reason of 1 to 1000 characters is required"))
		return
	}
	value, err := common.Marshal(request.Config)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if _, err = setting.ParseUserResourcesConfig(string(value)); err != nil {
		common.ApiError(c, err)
		return
	}
	before, err := model.UpdateUserResourcesConfig(string(value), request.ExpectedVersion)
	if errors.Is(err, model.ErrUserResourcesConflict) {
		common.ApiErrorStatus(c, http.StatusConflict, common.NewMessage(err.Error()))
		return
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}

	recordManageAudit(c, "user_resource_limits.update", map[string]any{"before": before, "after": request.Config, "reason": request.Reason})
	GetUserResourcesConfig(c)
}
