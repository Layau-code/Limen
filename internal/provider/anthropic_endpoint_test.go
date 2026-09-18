package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAnthropicProviderRejectsMismatchedEndpointBinding 确认错绑 endpoint 时不会发起上游请求。
func TestAnthropicProviderRejectsMismatchedEndpointBinding(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	client := NewAnthropic(server.Client(), server.URL, "anthropic-secret")
	response, err := client.Chat(context.Background(), ChatRequest{
		EndpointID: "endpoint:fedcba987654321001234567",
		Model:      "claude-test",
		Messages:   []Message{{Role: "user", Content: "hello"}},
	})
	if err == nil || response.Body != nil || calls != 0 {
		t.Fatalf("err=%v response body=%v upstream calls=%d", err, response.Body != nil, calls)
	}
}
