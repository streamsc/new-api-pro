package model

import (
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

const usageUserAgentKey = "usage_client_user_agent"

// CaptureUsageUserAgent snapshots the client header before relay/header overrides.
// Call only at synchronous/streaming relay entries, never at task submission or
// settlement. Store at most 1024 UTF-8 bytes, dropping a partial final character;
// malformed UTF-8 is replaced with U+FFFD so log JSON remains valid text.
func CaptureUsageUserAgent(c *gin.Context) {
	if _, captured := c.Get(usageUserAgentKey); captured {
		return
	}
	ua := strings.ToValidUTF8(c.Request.UserAgent(), "�")
	const maxBytes = 1024
	if len(ua) > maxBytes {
		end := maxBytes
		for !utf8.RuneStart(ua[end]) {
			end--
		}
		ua = ua[:end]
	}
	c.Set(usageUserAgentKey, ua)
}

func withUsageUserAgent(c *gin.Context, other *LogOther) *LogOther {
	ua, captured := c.Get(usageUserAgentKey)
	if !captured {
		return other
	}
	if other == nil {
		other = NewLogOther()
	}
	other.SetPublic("user_agent", ua)
	return other
}
