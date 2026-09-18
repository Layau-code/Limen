package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAnthropicChatConvertsRequestAndResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "anthropic-secret" {
			t.Errorf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Fatal("missing anthropic-version")
		}
		var request struct {
			Model    string    `json:"model"`
			System   string    `json:"system"`
			Messages []Message `json:"messages"`
			MaxToken int       `json:"max_tokens"`
			Temp     *float64  `json:"temperature"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "claude-test" || request.System != "be concise" || request.MaxToken != 64 || len(request.Messages) != 1 || request.Temp == nil {
			t.Fatalf("unexpected request: %+v", request)
		}
		w.Header().Set("request-id", "req-anthropic-1")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg-1","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`)
	}))
	defer server.Close()

	provider := NewAnthropic(server.Client(), server.URL, "anthropic-secret")
	response, err := provider.Chat(context.Background(), ChatRequest{
		Model: "claude-test",
		Messages: []Message{
			{Role: "system", Content: "be concise"},
			{Role: "user", Content: "hello"},
		},
		MaxTokens:   64,
		Temperature: float64Pointer(0.2),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"chat.completion"`) || !strings.Contains(string(body), `"hello"`) {
		t.Fatalf("response = %d %s", response.StatusCode, body)
	}
	if response.ProviderRequestID != "req-anthropic-1" {
		t.Fatalf("provider request id = %q", response.ProviderRequestID)
	}
	usage := response.Usage.Snapshot()
	if !usage.Complete || usage.InputTokens != 3 || usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v", usage)
	}
}

// TestAnthropicChatRejectsOversizedJSONResponse 防止异常上游响应无界占用内存。
func TestAnthropicChatRejectsOversizedJSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"msg-1","model":"claude-test","content":[{"type":"text","text":"`)
		_, _ = io.WriteString(w, strings.Repeat("x", 4<<20))
		_, _ = io.WriteString(w, `"}],"stop_reason":"end_turn"}`)
	}))
	defer server.Close()

	response, err := NewAnthropic(server.Client(), server.URL, "anthropic-secret").Chat(context.Background(), ChatRequest{
		Model:    "claude-test",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err == nil {
		if response.Body != nil {
			_ = response.Body.Close()
		}
		t.Fatal("expected oversized response error")
	}
	if !strings.Contains(err.Error(), "response is too large") {
		t.Fatalf("error = %v", err)
	}
}

func TestAnthropicChatMapsModernCompletionTokenLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			MaxTokens int `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.MaxTokens != 32 {
			t.Fatalf("max_tokens = %d", request.MaxTokens)
		}
		_, _ = io.WriteString(w, `{"id":"msg-1","model":"claude-test","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`)
	}))
	defer server.Close()

	limit := 32
	response, err := NewAnthropic(server.Client(), server.URL, "anthropic-secret").Chat(context.Background(), ChatRequest{
		Model:               "claude-test",
		Messages:            []Message{{Role: "user", Content: "hello"}},
		MaxCompletionTokens: &limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
}

func TestAnthropicChatMapsDeveloperMessageToSystem(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			System   string             `json:"system"`
			Messages []anthropicMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.System != "be precise" || len(request.Messages) != 1 || request.Messages[0].Role != "user" {
			t.Fatalf("request = %+v", request)
		}
		_, _ = io.WriteString(w, `{"id":"msg-1","model":"claude-test","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`)
	}))
	defer server.Close()

	response, err := NewAnthropic(server.Client(), server.URL, "anthropic-secret").Chat(context.Background(), ChatRequest{
		Model: "claude-test",
		Messages: []Message{
			{Role: "developer", Content: "be precise"},
			{Role: "user", Content: "hello"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
}

func TestAnthropicProviderRotatesAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "rotated" {
			t.Fatalf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		_, _ = io.WriteString(w, `{"id":"msg-1","model":"claude-test","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`)
	}))
	defer server.Close()
	client := NewAnthropic(server.Client(), server.URL, "initial")
	if err := client.SetAPIKey("rotated"); err != nil {
		t.Fatal(err)
	}
	response, err := client.Chat(context.Background(), ChatRequest{Model: "claude-test", Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if err := client.SetAPIKey(""); err == nil {
		t.Fatal("expected empty key rejection")
	}
}

func TestAnthropicProviderResolvesCredentialForRequestTenant(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "tenant-b-key" {
			t.Fatalf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		_, _ = io.WriteString(w, `{"id":"msg-1","model":"claude-test","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`)
	}))
	defer server.Close()
	client := NewAnthropic(server.Client(), server.URL, "shared-key")
	client.SetCredentialResolver(func(_ context.Context, tenantID string) (string, error) {
		if tenantID != "tenant-b" {
			t.Fatalf("tenant id = %q", tenantID)
		}
		return "tenant-b-key", nil
	})
	response, err := client.Chat(context.Background(), ChatRequest{TenantID: "tenant-b", Model: "claude-test", Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
}

func TestCredentialResolverRequiresTenantContext(t *testing.T) {
	_, err := resolveCredential(context.Background(), "", func(context.Context, string) (string, error) {
		return "tenant-key", nil
	}, "shared-key")
	if err == nil || !strings.Contains(err.Error(), "tenant context") {
		t.Fatalf("err=%v", err)
	}
}

func TestAnthropicChatConvertsStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\",\"model\":\"claude-test\",\"usage\":{\"input_tokens\":4}}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
		_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":6}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	provider := NewAnthropic(server.Client(), server.URL, "anthropic-secret")
	response, err := provider.Chat(context.Background(), ChatRequest{Model: "claude-test", Stream: true, Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	stream := string(body)
	if !strings.Contains(stream, `"delta":{"content":"hello"}`) || !strings.Contains(stream, `"finish_reason":"stop"`) || !strings.HasSuffix(stream, "data: [DONE]\n\n") {
		t.Fatalf("stream = %s", stream)
	}
	usage := response.Usage.Snapshot()
	if !usage.Complete || usage.InputTokens != 4 || usage.OutputTokens != 6 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestAnthropicStreamCloseCancelsUpstream(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()

	provider := NewAnthropic(server.Client(), server.URL, "anthropic-secret")
	response, err := provider.Chat(context.Background(), ChatRequest{Model: "claude-test", Stream: true, Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("upstream request did not start")
	}
	_ = response.Body.Close()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("closing stream did not cancel upstream")
	}
}

func TestAnthropicChatPreservesProviderErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"busy"}}`)
	}))
	defer server.Close()

	provider := NewAnthropic(server.Client(), server.URL, "anthropic-secret")
	response, err := provider.Chat(context.Background(), ChatRequest{Model: "claude-test", Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestClassifyHTTPStatus(t *testing.T) {
	tests := []struct {
		status int
		class  ErrorClass
	}{
		{http.StatusUnauthorized, ErrorClassAuthentication},
		{http.StatusPaymentRequired, ErrorClassQuota},
		{http.StatusBadRequest, ErrorClassDeterministicRequest},
		{http.StatusTooManyRequests, ErrorClassRetryableTransient},
		{http.StatusInternalServerError, ErrorClassRetryableTransient},
		{http.StatusNotImplemented, ErrorClassInternal},
	}
	for _, test := range tests {
		if got := ClassifyHTTPStatus(test.status); got != test.class {
			t.Errorf("status %d class = %q, want %q", test.status, got, test.class)
		}
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		class ErrorClass
	}{
		{name: "cancelled", err: context.Canceled, class: ErrorClassCancelled},
		{name: "request", err: &RequestError{Err: errors.New("invalid")}, class: ErrorClassDeterministicRequest},
		{name: "transport", err: &TransportError{Err: errors.New("network")}, class: ErrorClassRetryableTransient},
		{name: "internal", err: errors.New("unknown"), class: ErrorClassInternal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyError(test.err); got != test.class {
				t.Fatalf("class = %q, want %q", got, test.class)
			}
		})
	}
}

func TestAnthropicChatUsesOnlyCallerDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
		_, _ = io.WriteString(w, `{"id":"msg-1","model":"claude-test","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn"}`)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	provider := NewAnthropic(server.Client(), server.URL, "anthropic-secret")
	response, err := provider.Chat(ctx, ChatRequest{Model: "claude-test", Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
}

func TestAnthropicChatClassifiesCallErrors(t *testing.T) {
	tests := []struct {
		name     string
		provider *AnthropicProvider
		wantType string
	}{
		{
			name:     "request error",
			provider: NewAnthropic(http.DefaultClient, "://invalid", "anthropic-secret"),
			wantType: "*provider.RequestError",
		},
		{
			name: "transport error",
			provider: NewAnthropic(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("network unavailable")
			})}, "http://provider.example", "anthropic-secret"),
			wantType: "*provider.TransportError",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.provider.Chat(context.Background(), ChatRequest{Model: "claude-test", Messages: []Message{{Role: "user", Content: "hello"}}})
			if got := fmt.Sprintf("%T", err); got != test.wantType {
				t.Fatalf("error type = %s, want %s", got, test.wantType)
			}
		})
	}
}

func TestAnthropicChatClassifiesInvalidMessages(t *testing.T) {
	provider := NewAnthropic(http.DefaultClient, "http://provider.example", "anthropic-secret")
	_, err := provider.Chat(context.Background(), ChatRequest{Model: "claude-test", Messages: []Message{{Role: "system", Content: "only system"}}})
	if got := fmt.Sprintf("%T", err); got != "*provider.RequestError" {
		t.Fatalf("error type = %s, want *provider.RequestError", got)
	}
}

func TestAnthropicChatClassifiesInvalidResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "{")
	}))
	defer server.Close()

	client := NewAnthropic(server.Client(), server.URL, "anthropic-secret")
	_, err := client.Chat(context.Background(), ChatRequest{Model: "claude-test", Messages: []Message{{Role: "user", Content: "hello"}}})
	var requestError *RequestError
	if !errors.As(err, &requestError) {
		t.Fatalf("error = %T %v, want RequestError", err, err)
	}
}

func float64Pointer(value float64) *float64 { return &value }
