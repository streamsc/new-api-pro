package controller

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
)

func TestChannelConcurrencyErrorContract(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		statusCode int
		errorCode  types.ErrorCode
	}{
		{
			name:       "saturated channel returns 429",
			err:        service.ErrChannelConcurrencyLimit,
			statusCode: http.StatusTooManyRequests,
			errorCode:  types.ErrorCodeConcurrencyLimit,
		},
		{
			name:       "unavailable Redis returns 503",
			err:        service.ErrChannelConcurrencyStore,
			statusCode: http.StatusServiceUnavailable,
			errorCode:  types.ErrorCodeConcurrencyStore,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			relayErr := channelConcurrencyError(test.err)

			assert.Equal(t, test.statusCode, relayErr.StatusCode)
			assert.Equal(t, test.errorCode, relayErr.GetErrorCode())
			assert.True(t, types.IsSkipRetryError(relayErr))
			assert.False(t, types.IsRecordErrorLog(relayErr))
		})
	}
}
