package relay

import (
	"errors"
	"strconv"

	"github.com/QuantumNous/new-api/i18n"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
)

func AdmitUserResources(c *gin.Context, info *relaycommon.RelayInfo) (func(), *types.NewAPIError) {
	config, err := setting.CurrentUserResourcesConfig()
	if err != nil {
		return nil, types.NewErrorWithStatusCode(errors.New(i18n.T(c, i18n.MsgResourceStoreUnavailable)), types.ErrorCode("user_resource_store_unavailable"), 503, types.ErrOptionWithSkipRetry())
	}
	tokens, err := service.EstimateUserResourceRequest(c, info)
	if err != nil {
		return nil, types.NewErrorWithStatusCode(err, types.ErrorCodeCountTokenFailed, 400, types.ErrOptionWithSkipRetry())
	}

	release, rejection, err := service.AcquireUserResources(c.Request.Context(), info.UserId, tokens, config.ForUser(info.UserId))
	if err != nil {
		return nil, types.NewErrorWithStatusCode(errors.New(i18n.T(c, i18n.MsgResourceStoreUnavailable)), types.ErrorCode("user_resource_store_unavailable"), 503, types.ErrOptionWithSkipRetry())
	}
	if rejection != nil {
		if rejection.RetryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(rejection.RetryAfter))
		}
		return nil, types.NewErrorWithStatusCode(errors.New(i18n.T(c, "resources."+rejection.Code)), types.ErrorCode(rejection.Code), 429, types.ErrOptionWithSkipRetry())
	}
	return release, nil
}
