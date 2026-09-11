package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestForwardBuildsOpenAIRequest(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer provider-secret" {
			t.Errorf("authorization = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer provider.Close()

	service := New(provider.Client(), provider.URL+"/v1", "provider-secret", time.Second)
	response, err := service.Forward(context.Background(), []byte(`{"model":"gpt-test"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestForwardPropagatesCancellation(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	defer provider.Close()

	ctx, cancel := context.WithCancel(context.Background())
	service := New(provider.Client(), provider.URL, "provider-secret", time.Second)
	done := make(chan struct{})
	go func() {
		response, _ := service.Forward(ctx, []byte(`{"model":"gpt-test"}`))
		if response != nil {
			_ = response.Body.Close()
		}
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider request did not start")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("provider request was not canceled")
	}
	<-done
}

func TestForwardTimesOut(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		<-r.Context().Done()
	}))
	defer provider.Close()

	service := New(provider.Client(), provider.URL, "provider-secret", 10*time.Millisecond)
	response, err := service.Forward(context.Background(), []byte(`{"model":"gpt-test"}`))
	if response != nil {
		_ = response.Body.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}
