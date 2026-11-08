package controller

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

func diagnosticAPIContext(c *gin.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), 3*time.Second)
}
func diagnosticAPIError(c *gin.Context, err error) {
	status := http.StatusServiceUnavailable
	message := "Diagnostic operation failed"
	if errors.Is(err, model.ErrDiagnosticUnavailable) {
		status = http.StatusGone
		message = "Diagnostic task is unavailable or expired"
	}
	if errors.Is(err, model.ErrDiagnosticCapacity) {
		status = http.StatusConflict
		message = "Another diagnostic task is active or diagnostic storage is full"
	}
	common.ApiErrorStatus(c, status, common.NewMessage(message))
}
func diagnosticPage(c *gin.Context) (int, int) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	page = max(1, min(page, 100000))
	limit = max(1, min(limit, 100))
	return (page - 1) * limit, limit
}
func diagnosticID(c *gin.Context, key string) int64 {
	id, _ := strconv.ParseInt(c.Param(key), 10, 64)
	return id
}

func CreateAffinityDiagnostic(c *gin.Context) {
	svc := service.DiagnosticService()
	if svc == nil {
		diagnosticAPIError(c, errors.New("unavailable"))
		return
	}
	var input service.DiagnosticCreate
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	if err := common.DecodeJson(c.Request.Body, &input); err != nil {
		common.ApiErrorStatus(c, 400, common.NewMessage("Invalid diagnostic request"))
		return
	}
	ctx, cancel := diagnosticAPIContext(c)
	defer cancel()
	task, err := svc.Create(ctx, input, c.GetInt("id"))
	if err != nil {
		switch err.Error() {
		case "Invalid diagnostic limits", "Invalid excluded header name", "Diagnostic target user does not exist":
			common.ApiErrorStatus(c, 400, common.NewMessage(err.Error()))
		default:
			diagnosticAPIError(c, err)
		}
		return
	}
	common.ApiSuccess(c, task)
}
func ListAffinityDiagnostics(c *gin.Context) {
	ctx, cancel := diagnosticAPIContext(c)
	defer cancel()
	offset, limit := diagnosticPage(c)
	rows, total, err := model.ListAffinityDiagnosticTasks(ctx, offset, limit)
	if err != nil {
		diagnosticAPIError(c, err)
		return
	}
	svc := service.DiagnosticService()
	common.ApiSuccess(c, gin.H{"items": rows, "total": total, "storage_budget": model.DiagnosticStorageBudget, "propagation_seconds": 2, "redis_paused": svc != nil && svc.RedisPaused(), "node_name": common.NodeName})
}
func StopAffinityDiagnostic(c *gin.Context)   { changeAffinityDiagnostic(c, false) }
func DeleteAffinityDiagnostic(c *gin.Context) { changeAffinityDiagnostic(c, true) }
func changeAffinityDiagnostic(c *gin.Context, remove bool) {
	svc := service.DiagnosticService()
	if svc == nil {
		diagnosticAPIError(c, errors.New("unavailable"))
		return
	}
	ctx, cancel := diagnosticAPIContext(c)
	defer cancel()
	if err := svc.Stop(ctx, diagnosticID(c, "task"), remove); err != nil {
		diagnosticAPIError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"propagation_seconds": 2})
}
func ListAffinityDiagnosticSamples(c *gin.Context) {
	ctx, cancel := diagnosticAPIContext(c)
	defer cancel()
	offset, limit := diagnosticPage(c)
	rows, total, err := model.ListAffinityDiagnosticSamples(ctx, diagnosticID(c, "task"), offset, limit)
	if err != nil {
		diagnosticAPIError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"items": rows, "total": total})
}
func GetAffinityDiagnosticSample(c *gin.Context) {
	ctx, cancel := diagnosticAPIContext(c)
	defer cancel()
	sample, err := model.ReadAffinityDiagnosticSample(ctx, diagnosticID(c, "task"), diagnosticID(c, "sample"))
	if err != nil {
		diagnosticAPIError(c, err)
		return
	}
	if c.Query("download") == "body" {
		if sample.BodyStatus != "captured" {
			common.ApiErrorStatus(c, 409, common.NewMessage("Diagnostic body was not collected"))
			return
		}
		service.DiagnosticDownloadHeaders(c.Writer.Header())
		c.Data(200, "application/json", sample.Body)
		return
	}
	var metadata service.DiagnosticMetadata
	if err := common.Unmarshal(sample.Metadata, &metadata); err != nil {
		diagnosticAPIError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"sample": sample, "metadata": metadata, "body": string(sample.Body)})
}
func ExtractAffinityDiagnosticSample(c *gin.Context) {
	var source operation_setting.ChannelAffinityKeySource
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
	if err := common.DecodeJson(c.Request.Body, &source); err != nil || len(source.Path) > 4096 || len(source.Key) > 128 {
		common.ApiErrorStatus(c, 400, common.NewMessage("Invalid diagnostic extraction"))
		return
	}
	ctx, cancel := diagnosticAPIContext(c)
	defer cancel()
	sample, err := model.ReadAffinityDiagnosticSample(ctx, diagnosticID(c, "task"), diagnosticID(c, "sample"))
	if err != nil {
		diagnosticAPIError(c, err)
		return
	}
	var metadata service.DiagnosticMetadata
	if err := common.Unmarshal(sample.Metadata, &metadata); err != nil {
		diagnosticAPIError(c, err)
		return
	}
	common.ApiSuccess(c, service.EvaluateAffinityValue(service.DiagnosticSampleInput{Metadata: metadata, Body: sample.Body}, source))
}
