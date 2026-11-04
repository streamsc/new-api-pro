package controller

import (
	"errors"
	"net/http"
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
	usage, err := service.GetUserResourceUsage(c.Request.Context(), id)
	mode := "memory"
	if common.RedisEnabled {
		mode = "redis"
	}
	data := gin.H{"user_id": id, "username": user.Username, "limits": config.ForUser(id), "mode": mode, "available": err == nil, "updated_at": time.Now().Unix()}
	if err == nil {
		data["usage"] = usage
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}
func GetUserResourcesConfig(c *gin.Context) {
	config, err := setting.CurrentUserResourcesConfig()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, config)
}
func UpdateUserResourcesConfig(c *gin.Context) {
	var request struct {
		Config setting.UserResourcesConfig `json:"config"`
		Reason string                      `json:"reason"`
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
	before, err := model.UpdateUserResourcesConfig(string(value))
	if err != nil {
		common.ApiError(c, err)
		return
	}

	recordManageAudit(c, "user_resource_limits.update", map[string]any{"before": before, "after": request.Config, "reason": request.Reason})
	GetUserResourcesConfig(c)
}
