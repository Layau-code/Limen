package provider

import (
	"net/http"
	"strings"
	"unicode"
)

// providerRequestID 从上游响应提取可审计但不敏感的请求标识。
func providerRequestID(headers http.Header) string {
	for _, name := range []string{"x-request-id", "request-id", "anthropic-request-id"} {
		value := strings.TrimSpace(headers.Get(name))
		if value == "" || len(value) > 128 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			continue
		}
		return value
	}
	return ""
}
