package gateway

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCircuitBreakerOpensAndRecovers(t *testing.T) {
	now := time.Unix(1_000, 0)
	breaker := newCircuitBreaker(2, 30*time.Second, func() time.Time { return now })

	if !breaker.allow() {
		t.Fatal("closed circuit rejected first request")
	}
	breaker.recordFailure()
	if !breaker.allow() {
		t.Fatal("circuit opened before reaching threshold")
	}
	breaker.recordFailure()
	if breaker.allow() {
		t.Fatal("open circuit allowed request before cooldown")
	}

	now = now.Add(30 * time.Second)
	if !breaker.allow() {
		t.Fatal("circuit did not allow half-open probe")
	}
	if breaker.allow() {
		t.Fatal("circuit allowed a second half-open probe")
	}
	breaker.recordSuccess()
	if !breaker.allow() {
		t.Fatal("successful probe did not close circuit")
	}
}

func TestCircuitBreakerFailedProbeRestartsCooldown(t *testing.T) {
	now := time.Unix(1_000, 0)
	breaker := newCircuitBreaker(1, 30*time.Second, func() time.Time { return now })

	breaker.recordFailure()
	now = now.Add(30 * time.Second)
	if !breaker.allow() {
		t.Fatal("circuit did not allow half-open probe")
	}
	breaker.recordFailure()
	if breaker.allow() {
		t.Fatal("failed probe did not reopen circuit")
	}
	now = now.Add(30 * time.Second)
	if !breaker.allow() {
		t.Fatal("reopened circuit did not start a new cooldown")
	}
}

func TestCircuitBreakerAllowsSingleConcurrentProbe(t *testing.T) {
	now := time.Unix(1_000, 0)
	breaker := newCircuitBreaker(1, time.Second, func() time.Time { return now })
	breaker.recordFailure()
	now = now.Add(time.Second)

	var allowed atomic.Int32
	var group sync.WaitGroup
	for range 32 {
		group.Add(1)
		go func() {
			defer group.Done()
			if breaker.allow() {
				allowed.Add(1)
			}
		}()
	}
	group.Wait()
	if allowed.Load() != 1 {
		t.Fatalf("allowed probes = %d, want 1", allowed.Load())
	}
}
