package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huz/limen/internal/gateway"
	"github.com/huz/limen/internal/provider"
)

func TestChatRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name   string
		token  string
		body   string
		status int
	}{
		{"missing token", "", `{"model":"gpt-test"}`, http.StatusUnauthorized},
		{"wrong token", "wrong", `{"model":"gpt-test"}`, http.StatusUnauthorized},
		{"invalid json", "limen-secret", `{`, http.StatusBadRequest},
		{"missing model", "limen-secret", `{}`, http.StatusBadRequest},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := New("limen-secret", nil)
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(test.body))
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
			if got := response.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("content type = %q", got)
			}
		})
	}
}

func TestChatRelaysProviderResponse(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"chat-1"}`))
	}))
	defer providerServer.Close()

	openAI := provider.NewOpenAI(providerServer.Client(), providerServer.URL, "provider-secret", time.Second)
	handler := New("limen-secret", gateway.NewRouter(openAI, nil))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated || response.Body.String() != `{"id":"chat-1"}` {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestChatRelaysSSE(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: first\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer providerServer.Close()

	openAI := provider.NewOpenAI(providerServer.Client(), providerServer.URL, "provider-secret", time.Second)
	handler := New("limen-secret", gateway.NewRouter(openAI, nil))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}],"stream":true}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if got := response.Body.String(); got != "data: first\n\ndata: [DONE]\n\n" {
		t.Fatalf("stream = %q", got)
	}
}

func TestChatRelaysProviderError(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"busy"}}`))
	}))
	defer providerServer.Close()

	openAI := provider.NewOpenAI(providerServer.Client(), providerServer.URL, "provider-secret", time.Second)
	handler := New("limen-secret", gateway.NewRouter(openAI, nil))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusTooManyRequests || response.Body.String() != `{"error":{"message":"busy"}}` {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestChatRoutesClaudeAndRejectsUnknownModel(t *testing.T) {
	var claudeRequests int
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claudeRequests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg-1","model":"claude-test","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer providerServer.Close()

	router := gateway.NewRouter(nil, provider.NewAnthropic(providerServer.Client(), providerServer.URL, "anthropic-secret", time.Second))
	handler := New("limen-secret", router)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"claude-test","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || claudeRequests != 1 {
		t.Fatalf("status=%d claude_requests=%d", response.Code, claudeRequests)
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"unknown","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown model status = %d", response.Code)
	}
}

func TestChatRejectsUnsupportedContent(t *testing.T) {
	handler := New("limen-secret", nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unsupported content status = %d", response.Code)
	}
}
