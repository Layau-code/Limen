package provider

import (
	"context"
	"encoding/json"
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
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg-1","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`)
	}))
	defer server.Close()

	provider := NewAnthropic(server.Client(), server.URL, "anthropic-secret", time.Second)
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
}

func TestAnthropicChatConvertsStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\",\"model\":\"claude-test\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
		_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	provider := NewAnthropic(server.Client(), server.URL, "anthropic-secret", time.Second)
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

	provider := NewAnthropic(server.Client(), server.URL, "anthropic-secret", time.Second)
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

	provider := NewAnthropic(server.Client(), server.URL, "anthropic-secret", time.Second)
	response, err := provider.Chat(context.Background(), ChatRequest{Model: "claude-test", Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func float64Pointer(value float64) *float64 { return &value }
