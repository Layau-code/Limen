package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestOpenAIChatBuildsProviderRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer openai-secret" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"id":"chat-1"}`)
	}))
	defer server.Close()

	provider := NewOpenAI(server.Client(), server.URL+"/v1", "openai-secret")
	response, err := provider.Chat(context.Background(), ChatRequest{Model: "gpt-test", Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestOpenAIProviderRotatesAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer rotated" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	client := NewOpenAI(server.Client(), server.URL, "initial")
	if err := client.SetAPIKey("rotated"); err != nil {
		t.Fatal(err)
	}
	response, err := client.Chat(context.Background(), ChatRequest{Model: "gpt-test", Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if err := client.SetAPIKey(" "); err == nil {
		t.Fatal("expected empty key rejection")
	}
}

func TestOpenAIProviderResolvesCredentialForRequestTenant(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tenant-b-key" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	client := NewOpenAI(server.Client(), server.URL, "shared-key")
	client.SetCredentialResolver(func(_ context.Context, tenantID string) (string, error) {
		if tenantID != "tenant-b" {
			t.Fatalf("tenant id = %q", tenantID)
		}
		return "tenant-b-key", nil
	})
	response, err := client.Chat(context.Background(), ChatRequest{TenantID: "tenant-b", Model: "gpt-test", Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
}

func TestOpenAIProviderDoesNotFallbackWhenTenantCredentialIsMissing(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	client := NewOpenAI(server.Client(), server.URL, "shared-key")
	client.SetCredentialResolver(func(context.Context, string) (string, error) {
		return "", errors.New("credential not found")
	})
	_, err := client.Chat(context.Background(), ChatRequest{TenantID: "tenant-b", Model: "gpt-test", Messages: []Message{{Role: "user", Content: "hello"}}})
	if err == nil || calls != 0 {
		t.Fatalf("err=%v upstream calls=%d", err, calls)
	}
}

func TestOpenAIChatCollectsUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"chat-1","usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	}))
	defer server.Close()

	client := NewOpenAI(server.Client(), server.URL, "openai-secret")
	response, err := client.Chat(context.Background(), ChatRequest{Model: "gpt-test", Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatal(err)
	}
	usage := response.Usage.Snapshot()
	if !usage.Complete || usage.InputTokens != 3 || usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestOpenAIStreamRequestsAndCollectsUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			StreamOptions struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if !request.StreamOptions.IncludeUsage {
			t.Error("stream usage option was not enabled")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":6}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewOpenAI(server.Client(), server.URL, "openai-secret")
	response, err := client.Chat(context.Background(), ChatRequest{Model: "gpt-test", Stream: true, Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatal(err)
	}
	usage := response.Usage.Snapshot()
	if !usage.Complete || usage.InputTokens != 4 || usage.OutputTokens != 6 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestOpenAIStreamCanDisableUsageOption(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if _, found := request["stream_options"]; found {
			t.Fatal("stream_options should be omitted")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	includeUsage := false
	response, err := NewOpenAI(server.Client(), server.URL, "openai-secret").Chat(context.Background(), ChatRequest{
		Model:              "gpt-test",
		Messages:           []Message{{Role: "user", Content: "hello"}},
		Stream:             true,
		StreamIncludeUsage: &includeUsage,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatal(err)
	}
}

func TestOpenAIProviderForwardsModernCompletionTokenLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if _, found := request["max_tokens"]; found {
			t.Fatal("max_tokens should be omitted when max_completion_tokens is set")
		}
		var limit int
		if err := json.Unmarshal(request["max_completion_tokens"], &limit); err != nil || limit != 32 {
			t.Fatalf("max_completion_tokens = %s", request["max_completion_tokens"])
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	limit := 32
	response, err := NewOpenAI(server.Client(), server.URL, "openai-secret").Chat(context.Background(), ChatRequest{
		Model:               "o3-test",
		Messages:            []Message{{Role: "user", Content: "hello"}},
		MaxCompletionTokens: &limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
}

func TestOpenAIChatCancellationReachesProvider(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	provider := NewOpenAI(server.Client(), server.URL, "openai-secret")
	done := make(chan struct{})
	go func() {
		_, _ = provider.Chat(ctx, ChatRequest{Model: "gpt-test", Messages: []Message{{Role: "user", Content: "hello"}}})
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("upstream request did not start")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not reach provider")
	}
	<-done
}

func TestOpenAIChatUsesOnlyCallerDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
		_, _ = io.WriteString(w, `{"id":"chat-1"}`)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	provider := NewOpenAI(server.Client(), server.URL, "openai-secret")
	response, err := provider.Chat(ctx, ChatRequest{Model: "gpt-test", Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
}

func TestOpenAIChatCapturesProviderRequestID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-request-id", "req-openai-1")
		_, _ = io.WriteString(w, `{"id":"chat-1"}`)
	}))
	defer server.Close()

	response, err := NewOpenAI(server.Client(), server.URL, "openai-secret").Chat(context.Background(), ChatRequest{Model: "gpt-test", Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.ProviderRequestID != "req-openai-1" {
		t.Fatalf("provider request id = %q", response.ProviderRequestID)
	}
}

func TestOpenAIChatClassifiesCallErrors(t *testing.T) {
	tests := []struct {
		name     string
		provider *OpenAIProvider
		wantType string
	}{
		{
			name:     "request error",
			provider: NewOpenAI(http.DefaultClient, "://invalid", "openai-secret"),
			wantType: "*provider.RequestError",
		},
		{
			name: "transport error",
			provider: NewOpenAI(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("network unavailable")
			})}, "http://provider.example", "openai-secret"),
			wantType: "*provider.TransportError",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.provider.Chat(context.Background(), ChatRequest{Model: "gpt-test", Messages: []Message{{Role: "user", Content: "hello"}}})
			if got := fmt.Sprintf("%T", err); got != test.wantType {
				t.Fatalf("error type = %s, want %s", got, test.wantType)
			}
		})
	}
}
