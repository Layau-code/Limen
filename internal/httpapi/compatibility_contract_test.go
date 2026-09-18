package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huz/limen/internal/gateway"
	"github.com/huz/limen/internal/provider"
)

func TestOpenAICompatibilityRequestContract(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		status    int
		errorCode string
	}{
		{
			name:   "supported chat subset",
			body:   `{"model":"gpt-contract","messages":[{"role":"user","content":"hello"}],"max_tokens":16,"temperature":0.2}`,
			status: http.StatusOK,
		},
		{
			name:   "modern completion token limit",
			body:   `{"model":"gpt-contract","messages":[{"role":"user","content":"hello"}],"max_completion_tokens":16}`,
			status: http.StatusOK,
		},
		{
			name:      "completion token limits are mutually exclusive",
			body:      `{"model":"gpt-contract","messages":[{"role":"user","content":"hello"}],"max_tokens":16,"max_completion_tokens":16}`,
			status:    http.StatusBadRequest,
			errorCode: "invalid_chat_request",
		},
		{
			name:   "stream usage option",
			body:   `{"model":"gpt-contract","messages":[{"role":"user","content":"hello"}],"stream":true,"stream_options":{"include_usage":true}}`,
			status: http.StatusOK,
		},
		{
			name:      "stream usage option requires stream",
			body:      `{"model":"gpt-contract","messages":[{"role":"user","content":"hello"}],"stream_options":{"include_usage":true}}`,
			status:    http.StatusBadRequest,
			errorCode: "invalid_chat_request",
		},
		{
			name:      "stream usage option requires include usage",
			body:      `{"model":"gpt-contract","messages":[{"role":"user","content":"hello"}],"stream":true,"stream_options":{}}`,
			status:    http.StatusBadRequest,
			errorCode: "invalid_chat_request",
		},
		{
			name:      "stream usage option rejects unknown nested fields",
			body:      `{"model":"gpt-contract","messages":[{"role":"user","content":"hello"}],"stream":true,"stream_options":{"include_usage":true,"extra":true}}`,
			status:    http.StatusBadRequest,
			errorCode: "unsupported_field",
		},
		{
			name:      "tools are rejected",
			body:      `{"model":"gpt-contract","messages":[{"role":"user","content":"hello"}],"tools":[]}`,
			status:    http.StatusBadRequest,
			errorCode: "unsupported_field",
		},
		{
			name:      "response format is rejected",
			body:      `{"model":"gpt-contract","messages":[{"role":"user","content":"hello"}],"response_format":{"type":"json_object"}}`,
			status:    http.StatusBadRequest,
			errorCode: "unsupported_field",
		},
		{
			name:      "unknown fields are rejected",
			body:      `{"model":"gpt-contract","messages":[{"role":"user","content":"hello"}],"unknown":true}`,
			status:    http.StatusBadRequest,
			errorCode: "unsupported_field",
		},
		{
			name:      "tool calls are rejected",
			body:      `{"model":"gpt-contract","messages":[{"role":"assistant","content":"hello","tool_calls":[]}]}`,
			status:    http.StatusBadRequest,
			errorCode: "unsupported_field",
		},
		{
			name:      "multimodal content is rejected",
			body:      `{"model":"gpt-contract","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`,
			status:    http.StatusBadRequest,
			errorCode: "invalid_chat_request",
		},
	}
	providerCalls := 0
	upstream := testProviderFunc(func(_ context.Context, _ provider.ChatRequest) (provider.Response, error) {
		providerCalls++
		return provider.Response{StatusCode: http.StatusOK, ContentType: "application/json", Body: io.NopCloser(strings.NewReader(`{"id":"chat-contract"}`))}, nil
	})
	handler := New("secret", newTestRouter(upstream, nil, gateway.NewCompatibilityRegistry()))
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer secret")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.status, response.Body.String())
			}
			if test.errorCode == "" {
				return
			}
			var envelope errorEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Type != "invalid_request_error" || envelope.Error.Code != test.errorCode {
				t.Fatalf("error = %+v", envelope.Error)
			}
		})
	}
	if providerCalls != 3 {
		t.Fatalf("provider calls = %d, want 3", providerCalls)
	}
}

func TestOpenAICompatibilityStreamContract(t *testing.T) {
	upstream := testProviderFunc(func(_ context.Context, _ provider.ChatRequest) (provider.Response, error) {
		return provider.Response{
			StatusCode:  http.StatusOK,
			ContentType: "text/event-stream",
			Body:        io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n")),
		}, nil
	})
	handler := New("secret", newTestRouter(upstream, nil, gateway.NewCompatibilityRegistry()))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-contract","messages":[{"role":"user","content":"hello"}],"stream":true}`))
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") || !strings.HasSuffix(response.Body.String(), "data: [DONE]\n\n") {
		t.Fatalf("status=%d content-type=%q body=%q", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
}
