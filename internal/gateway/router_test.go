package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huz/limen/internal/provider"
)

func TestRouterSelectsProviderByModelPrefix(t *testing.T) {
	var openAIRequests, anthropicRequests int
	openAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openAIRequests++
		_, _ = io.WriteString(w, `{"id":"openai"}`)
	}))
	defer openAI.Close()
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anthropicRequests++
		_, _ = io.WriteString(w, `{"id":"anthropic"}`)
	}))
	defer anthropic.Close()

	router := NewRouter(provider.NewOpenAI(openAI.Client(), openAI.URL, "openai-secret", 0), provider.NewAnthropic(anthropic.Client(), anthropic.URL, "anthropic-secret", 0))
	for _, model := range []string{"gpt-test", "o1-test", "o3-test", "claude-test"} {
		response, err := router.Chat(context.Background(), provider.ChatRequest{Model: model, Messages: []provider.Message{{Role: "user", Content: "hello"}}})
		if err != nil {
			t.Fatalf("model %s: %v", model, err)
		}
		_ = response.Body.Close()
	}
	if openAIRequests != 3 || anthropicRequests != 1 {
		t.Fatalf("openai=%d anthropic=%d", openAIRequests, anthropicRequests)
	}
}

func TestRouterRejectsUnknownModel(t *testing.T) {
	router := NewRouter(nil, nil)
	_, err := router.Chat(context.Background(), provider.ChatRequest{Model: "unknown-model"})
	if err == nil {
		t.Fatal("expected unsupported model error")
	}
	if _, ok := err.(*UnsupportedModelError); !ok {
		t.Fatalf("error type = %T", err)
	}
}
