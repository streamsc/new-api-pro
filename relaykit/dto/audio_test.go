package dto

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAudioRequestIsStream(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		raw      string
		expected bool
	}{
		{
			name:     "transcription parsed multipart stream true",
			path:     "/v1/audio/transcriptions",
			raw:      `{"stream":"true"}`,
			expected: true,
		},
		{
			name:     "translation json stream true",
			path:     "/v1/audio/translations",
			raw:      `{"stream":true}`,
			expected: true,
		},
		{
			name:     "transcription stream false",
			path:     "/v1/audio/transcriptions",
			raw:      `{"stream":"false"}`,
			expected: false,
		},
		{
			name:     "transcription stream missing",
			path:     "/v1/audio/transcriptions",
			raw:      `{}`,
			expected: false,
		},
		{
			name:     "speech stream true does not trigger stt stream",
			path:     "/v1/audio/speech",
			raw:      `{"stream":"true"}`,
			expected: false,
		},
		{
			name:     "stream format sse keeps existing behavior",
			path:     "/v1/audio/speech",
			raw:      `{"stream_format":"sse"}`,
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &AudioRequest{}
			require.NoError(t, json.Unmarshal([]byte(tt.raw), req))
			httpReq := httptest.NewRequest(http.MethodPost, tt.path, nil)
			require.Equal(t, tt.expected, req.IsStream(httpReq))
		})
	}
}
