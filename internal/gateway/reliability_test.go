package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/huz/limen/internal/provider"
)

func TestRouterDoesNotFallbackOnDeterministicStatus(t *testing.T) {
	var backupCalls int
	providers := map[string]provider.Provider{
		"openai": providerFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
			return provider.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(`{"error":"invalid"}`))}, nil
		}),
		"anthropic": providerFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
			backupCalls++
			return provider.Response{}, nil
		}),
	}
	router := newReliabilityRouter(t, providers)
	result, err := router.Chat(context.Background(), provider.ChatRequest{Model: "smart-model"})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Response.Body.Close()
	if result.Response.StatusCode != http.StatusBadRequest || backupCalls != 0 {
		t.Fatalf("status=%d backup_calls=%d", result.Response.StatusCode, backupCalls)
	}
}

func TestRouterDoesNotFallbackOnRequestError(t *testing.T) {
	var backupCalls int
	providers := map[string]provider.Provider{
		"openai": providerFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
			return provider.Response{}, &provider.RequestError{Operation: "encode", Err: errors.New("invalid request")}
		}),
		"anthropic": providerFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
			backupCalls++
			return provider.Response{}, nil
		}),
	}
	router := newReliabilityRouter(t, providers)
	_, err := router.Chat(context.Background(), provider.ChatRequest{Model: "smart-model"})
	var routeError *RouteError
	if !errors.As(err, &routeError) || backupCalls != 0 {
		t.Fatalf("error=%v backup_calls=%d", err, backupCalls)
	}
	if routeError.Decision.String() != "openai:request_error" {
		t.Fatalf("decision = %s", routeError.Decision.String())
	}
}

func TestRouterFallsBackAfterAttemptTimeout(t *testing.T) {
	providers := map[string]provider.Provider{
		"openai": providerFunc(func(ctx context.Context, request provider.ChatRequest) (provider.Response, error) {
			<-ctx.Done()
			return provider.Response{}, &provider.TransportError{Operation: "send", Err: ctx.Err()}
		}),
		"anthropic": providerFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
			return provider.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"id":"ok"}`))}, nil
		}),
	}
	router := newReliabilityRouterWithPolicy(t, providers, Policy{
		RequestTimeout: time.Second, AttemptTimeout: 10 * time.Millisecond,
		FailureThreshold: 3, Cooldown: time.Second,
	})
	result, err := router.Chat(context.Background(), provider.ChatRequest{Model: "smart-model"})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Response.Body.Close()
	if result.Decision.String() != "openai:timeout>anthropic:200" {
		t.Fatalf("decision = %s", result.Decision.String())
	}
}

func TestRouterStopsFallbackWhenTotalBudgetExpires(t *testing.T) {
	var backupCalls int
	providers := map[string]provider.Provider{
		"openai": providerFunc(func(ctx context.Context, request provider.ChatRequest) (provider.Response, error) {
			<-ctx.Done()
			return provider.Response{}, &provider.TransportError{Operation: "send", Err: ctx.Err()}
		}),
		"anthropic": providerFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
			backupCalls++
			return provider.Response{}, nil
		}),
	}
	router := newReliabilityRouterWithPolicy(t, providers, Policy{
		RequestTimeout: 10 * time.Millisecond, AttemptTimeout: time.Second,
		FailureThreshold: 3, Cooldown: time.Second,
	})
	_, err := router.Chat(context.Background(), provider.ChatRequest{Model: "smart-model"})
	var routeError *RouteError
	if !errors.As(err, &routeError) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %T %v", err, err)
	}
	if backupCalls != 0 || routeError.Decision.String() != "openai:timeout" {
		t.Fatalf("backup_calls=%d decision=%s", backupCalls, routeError.Decision.String())
	}
}

func TestRouterReturnsLastResponseWhenRemainingTargetIsOpen(t *testing.T) {
	primaryBody := &closeSpy{Reader: strings.NewReader(`{"error":"busy"}`)}
	providers := map[string]provider.Provider{
		"openai": providerFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
			return provider.Response{StatusCode: http.StatusTooManyRequests, Body: primaryBody}, nil
		}),
		"anthropic": providerFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
			t.Fatal("open fallback target was called")
			return provider.Response{}, nil
		}),
	}
	router := newReliabilityRouterWithPolicy(t, providers, Policy{
		RequestTimeout: time.Second, AttemptTimeout: time.Second,
		FailureThreshold: 1, Cooldown: time.Hour,
	})
	model, _ := router.registry.Resolve("smart-model")
	router.breakers[targetKey(model, model.Targets[1])].recordFailure()

	result, err := router.Chat(context.Background(), provider.ChatRequest{Model: "smart-model"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Response.StatusCode != http.StatusTooManyRequests || primaryBody.closed {
		t.Fatalf("status=%d body_closed=%t", result.Response.StatusCode, primaryBody.closed)
	}
	if result.Decision.String() != "openai:429>anthropic:circuit_open" {
		t.Fatalf("decision = %s", result.Decision.String())
	}
	_ = result.Response.Body.Close()
}

func TestRouterDoesNotReplayStartedStream(t *testing.T) {
	var backupCalls int
	streamError := errors.New("stream interrupted")
	providers := map[string]provider.Provider{
		"openai": providerFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
			return provider.Response{StatusCode: http.StatusOK, ContentType: "text/event-stream", Body: &errorBody{err: streamError}}, nil
		}),
		"anthropic": providerFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
			backupCalls++
			return provider.Response{}, nil
		}),
	}
	router := newReliabilityRouter(t, providers)
	result, err := router.Chat(context.Background(), provider.ChatRequest{Model: "smart-model", Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Response.Body.Close()
	_, readErr := result.Response.Body.Read(make([]byte, 1))
	if !errors.Is(readErr, streamError) || backupCalls != 0 {
		t.Fatalf("read_error=%v backup_calls=%d", readErr, backupCalls)
	}
}

func TestRouterSharesOneDeadlineAcrossAttempts(t *testing.T) {
	var deadlines []time.Time
	captureDeadline := func(ctx context.Context, request provider.ChatRequest) (provider.Response, error) {
		deadline, found := ctx.Deadline()
		if !found {
			t.Fatal("attempt context has no deadline")
		}
		deadlines = append(deadlines, deadline)
		return provider.Response{}, &provider.TransportError{Operation: "send", Err: errors.New("network unavailable")}
	}
	providers := map[string]provider.Provider{
		"openai":    providerFunc(captureDeadline),
		"anthropic": providerFunc(captureDeadline),
	}
	router := newReliabilityRouterWithPolicy(t, providers, Policy{
		RequestTimeout: 100 * time.Millisecond, AttemptTimeout: time.Second,
		FailureThreshold: 3, Cooldown: time.Second,
	})
	_, err := router.Chat(context.Background(), provider.ChatRequest{Model: "smart-model"})
	if err == nil {
		t.Fatal("expected exhausted route error")
	}
	if len(deadlines) != 2 || !deadlines[0].Equal(deadlines[1]) {
		t.Fatalf("attempt deadlines = %v", deadlines)
	}
}

func TestRouterPropagatesCallerCancellation(t *testing.T) {
	var backupCalls int
	started := make(chan struct{})
	providers := map[string]provider.Provider{
		"openai": providerFunc(func(ctx context.Context, request provider.ChatRequest) (provider.Response, error) {
			close(started)
			<-ctx.Done()
			return provider.Response{}, &provider.TransportError{Operation: "send", Err: ctx.Err()}
		}),
		"anthropic": providerFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
			backupCalls++
			return provider.Response{}, nil
		}),
	}
	router := newReliabilityRouter(t, providers)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := router.Chat(ctx, provider.ChatRequest{Model: "smart-model"})
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if backupCalls != 0 {
		t.Fatalf("backup calls = %d", backupCalls)
	}
}

func TestRouterResponseCloseCancelsAttempt(t *testing.T) {
	attemptContext := make(chan context.Context, 1)
	providers := map[string]provider.Provider{
		"openai": providerFunc(func(ctx context.Context, request provider.ChatRequest) (provider.Response, error) {
			attemptContext <- ctx
			return provider.Response{StatusCode: http.StatusOK, Body: &waitForCancelBody{ctx: ctx}}, nil
		}),
	}
	registry, err := NewModelRegistry([]Model{{ID: "smart-model", Targets: []Target{{Provider: "openai", UpstreamModel: "gpt-real"}}}})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(providers, registry, Policy{RequestTimeout: 200 * time.Millisecond, AttemptTimeout: 200 * time.Millisecond, FailureThreshold: 3, Cooldown: time.Second})
	result, err := router.Chat(context.Background(), provider.ChatRequest{Model: "smart-model"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := <-attemptContext
	closed := make(chan error, 1)
	go func() { closed <- result.Response.Body.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("response close waited for the request deadline")
	}
	if ctx.Err() == nil {
		t.Fatal("closing response did not cancel attempt context")
	}
}

func TestRouterSkipsOpenCircuitAndRecoversAfterCooldown(t *testing.T) {
	now := time.Unix(1_000, 0)
	primaryCalls := 0
	backupCalls := 0
	providers := map[string]provider.Provider{
		"openai": providerFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
			primaryCalls++
			status := http.StatusOK
			if primaryCalls == 1 {
				status = http.StatusServiceUnavailable
			}
			return provider.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}),
		"anthropic": providerFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
			backupCalls++
			return provider.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}),
	}
	registry, err := NewModelRegistry([]Model{{ID: "smart-model", Targets: []Target{
		{Provider: "openai", UpstreamModel: "gpt-real"},
		{Provider: "anthropic", UpstreamModel: "claude-real"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	router := newRouter(providers, registry, Policy{
		RequestTimeout: time.Second, AttemptTimeout: time.Second,
		FailureThreshold: 1, Cooldown: 30 * time.Second,
	}, func() time.Time { return now })

	first, err := router.Chat(context.Background(), provider.ChatRequest{Model: "smart-model"})
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Response.Body.Close()
	second, err := router.Chat(context.Background(), provider.ChatRequest{Model: "smart-model"})
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Response.Body.Close()
	if second.Decision.String() != "openai:circuit_open>anthropic:200" || primaryCalls != 1 || backupCalls != 2 {
		t.Fatalf("second=%s primary=%d backup=%d", second.Decision.String(), primaryCalls, backupCalls)
	}

	now = now.Add(30 * time.Second)
	third, err := router.Chat(context.Background(), provider.ChatRequest{Model: "smart-model"})
	if err != nil {
		t.Fatal(err)
	}
	_ = third.Response.Body.Close()
	if third.Decision.String() != "openai:200" || primaryCalls != 2 || backupCalls != 2 {
		t.Fatalf("third=%s primary=%d backup=%d", third.Decision.String(), primaryCalls, backupCalls)
	}
}

func TestCompatibilityRouteHasStableCircuitBreaker(t *testing.T) {
	registry := NewCompatibilityRegistry()
	router := NewRouter(map[string]provider.Provider{"openai": providerFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
		return provider.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}, registry, Policy{RequestTimeout: time.Second, AttemptTimeout: time.Second, FailureThreshold: 3, Cooldown: time.Second})
	model, found := registry.Resolve("gpt-test")
	if !found {
		t.Fatal("compatibility model was not resolved")
	}
	if router.breakers[targetKey(model, model.Targets[0])] == nil {
		t.Fatal("resolved compatibility target has no circuit breaker")
	}
}

func newReliabilityRouter(t *testing.T, providers map[string]provider.Provider) *Router {
	t.Helper()
	return newReliabilityRouterWithPolicy(t, providers, Policy{
		RequestTimeout: time.Second, AttemptTimeout: time.Second,
		FailureThreshold: 3, Cooldown: time.Second,
	})
}

func newReliabilityRouterWithPolicy(t *testing.T, providers map[string]provider.Provider, policy Policy) *Router {
	t.Helper()
	registry, err := NewModelRegistry([]Model{{ID: "smart-model", Targets: []Target{
		{Provider: "openai", UpstreamModel: "gpt-real"},
		{Provider: "anthropic", UpstreamModel: "claude-real"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(providers, registry, policy)
}

type errorBody struct {
	err error
}

func (body *errorBody) Read([]byte) (int, error) {
	return 0, body.err
}

func (body *errorBody) Close() error {
	return nil
}

type waitForCancelBody struct {
	ctx context.Context
}

func (body *waitForCancelBody) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (body *waitForCancelBody) Close() error {
	<-body.ctx.Done()
	return nil
}
