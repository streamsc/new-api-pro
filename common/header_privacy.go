package common

import "strings"

// ExcludeCredentialHeader is shared by persistent usage logs and temporary
// diagnostics. Extra exclusions can only add to the built-in credential list.
func ExcludeCredentialHeader(name string, extra []string) bool {
	switch strings.ToLower(name) {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "api-key", "apikey", "x-goog-api-key", "mj-api-secret", "x-auth-token", "x-access-token", "sec-websocket-protocol":
		return true
	}
	for _, excluded := range extra {
		if strings.EqualFold(name, excluded) {
			return true
		}
	}
	return false
}
