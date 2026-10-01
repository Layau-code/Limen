package semantic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huz/limen/internal/config"
)

const acceptanceJevResponse = `{"model":"jev-1.13.0","answers":{"task_type":{"type":"choice","choice":"code","confidence":0.9,"probabilities":{"extraction":0.02,"transformation":0.02,"writing":0.02,"code":0.9,"analysis":0.02,"other":0.02}},"complexity":{"type":"score","score":1.8,"confidence":0.85,"probabilities":{"0":0.05,"1":0.1,"2":0.85}}},"usage":{"input_tokens":24,"output_tokens":9}}`

type acceptanceTransport func(*http.Request) (*http.Response, error)

func (transport acceptanceTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return transport(r)
}

func TestJevHTTPContractAndFaults(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
		stall            bool
	}{
		{name: "valid", body: acceptanceJevResponse, status: 200},
		{name: "rate_limit", body: "private vendor error", status: 429, want: "provider_error"},
		{name: "invalid_json", body: "{", status: 200, want: "invalid_response"},
		{name: "wrong_version", body: strings.ReplaceAll(acceptanceJevResponse, "jev-1.13.0", "jev-1.12.0"), status: 200, want: "version_mismatch"},
		{name: "response_limit", body: strings.Repeat("x", jevMaxResponseBytes+1), status: 200, want: "invalid_response"},
		{name: "body_timeout", status: 200, stall: true, want: "provider_timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Error("invalid Jev wire request")
				}
				var body jevRequest
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.State != "user: classify me" || body.Model != "jev-1.13.0" || body.Questions["task_type"].Type != "choice" || body.Questions["complexity"].Type != "score" {
					t.Error("request schema changed")
				}
				w.WriteHeader(tc.status)
				if tc.stall {
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			transport := server.Client().Transport
			localURL, _ := url.Parse(server.URL)
			client := NewJevClient(&http.Client{Transport: acceptanceTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != jevEndpoint+"/v1/systemone" {
					t.Error("adapter changed fixed endpoint")
				}
				copy := r.Clone(r.Context())
				copy.URL.Scheme, copy.URL.Host = localURL.Scheme, localURL.Host
				return transport.RoundTrip(copy)
			})}, "test-key", nil)
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			got, err := client.Assess(ctx, "tenant", State{Text: "user: classify me", Language: "en"}, "jev-1.13.0", "task-complexity.en.v1")
			if tc.want == "" {
				if err != nil || got.TaskType != "code" || got.InputTokens != 24 {
					t.Fatalf("assessment=%+v err=%v", got, err)
				}
			} else if err == nil || StatusForError(err) != tc.want {
				t.Fatalf("err=%v status=%s want=%s", err, StatusForError(err), tc.want)
			}
			if calls.Load() != 1 {
				t.Fatalf("hot path retried: %d", calls.Load())
			}
		})
	}
}

func TestJevCircuitBreakerAndConcurrency(t *testing.T) {
	var calls atomic.Int32
	client := NewJevClient(&http.Client{Transport: acceptanceTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}, "key", nil)
	now := time.Now()
	client.now = func() time.Time { return now }
	for i := 0; i < 4; i++ {
		_, _ = client.Assess(context.Background(), "tenant", State{Text: "x"}, "jev-1.13.0", "task-complexity.en.v1")
	}
	if calls.Load() != 3 {
		t.Fatalf("breaker sent %d requests", calls.Load())
	}
	now = now.Add(2 * time.Second)
	_, _ = client.Assess(context.Background(), "tenant", State{Text: "x"}, "jev-1.13.0", "task-complexity.en.v1")
	if calls.Load() != 4 {
		t.Fatal("breaker failed to recover")
	}

	entered := make(chan struct{}, 64)
	release := make(chan struct{})
	var active, peak atomic.Int32
	client = NewJevClient(&http.Client{Transport: acceptanceTransport(func(r *http.Request) (*http.Response, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(acceptanceJevResponse)), Header: make(http.Header)}, nil
	})}, "key", nil)
	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = client.Assess(ctx, "tenant", State{Text: "x"}, "jev-1.13.0", "task-complexity.en.v1")
		}()
	}
	for i := 0; i < 32; i++ {
		select {
		case <-entered:
		case <-ctx.Done():
			close(release)
			wg.Wait()
			t.Fatal("workers did not enter")
		}
	}
	close(release)
	wg.Wait()
	if peak.Load() > 32 {
		t.Fatalf("concurrency limit exceeded: %d", peak.Load())
	}
}

type acceptanceAssessor func(context.Context, string, State, string, string) (Assessment, error)

func (fn acceptanceAssessor) Assess(ctx context.Context, tenant string, state State, model, template string) (Assessment, error) {
	return fn(ctx, tenant, state, model, template)
}

type acceptanceShadowStore func(context.Context, ShadowEvaluation) error

func (fn acceptanceShadowStore) SaveShadowEvaluation(ctx context.Context, value ShadowEvaluation) error {
	return fn(ctx, value)
}

func TestShadowQueueIsBoundedAndCloseCancels(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	worker := NewShadowWorker(acceptanceAssessor(func(ctx context.Context, _ string, _ State, _, _ string) (Assessment, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-ctx.Done()
		return Assessment{}, ctx.Err()
	}), NewMemoryShadowStore(), nil, 1, 1)
	job := ShadowJob{State: State{Text: "private in-memory state"}, Config: config.SemanticRouting{Timeout: time.Second}}
	if !worker.Submit(job) {
		t.Fatal("first submit failed")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker not started")
	}
	if !worker.Submit(job) || worker.Submit(job) {
		t.Fatal("queue is not bounded")
	}
	worker.Close()
	if worker.Submit(job) || len(worker.queue) != 0 || calls.Load() != 1 {
		t.Fatal("shutdown did not discard pending work")
	}
}

func TestShadowStorageFailureIsObservable(t *testing.T) {
	done := make(chan ShadowEvaluation, 1)
	worker := NewShadowWorker(acceptanceAssessor(func(context.Context, string, State, string, string) (Assessment, error) {
		return decodeJevResponse([]byte(acceptanceJevResponse), "jev-1.13.0")
	}), acceptanceShadowStore(func(context.Context, ShadowEvaluation) error { return errors.New("store offline") }), nil, 1, 1)
	defer worker.Close()
	worker.SetObserver(func(value ShadowEvaluation) { done <- value })
	worker.Submit(ShadowJob{State: State{Text: "test", Language: "en"}})
	select {
	case got := <-done:
		if got.Status != "storage_error" {
			t.Fatalf("lost sample reported as %s", got.Status)
		}
	case <-time.After(time.Second):
		t.Fatal("missing metrics")
	}
}
