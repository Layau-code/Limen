package provider

import (
	"context"
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
