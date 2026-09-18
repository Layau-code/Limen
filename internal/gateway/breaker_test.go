package gateway

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCircuitBreakerOpensAndRecovers(t *testing.T) {
	now := time.Unix(1_000, 0)
	breaker := newCircuitBreaker(func() time.Time { return now })

	if !breaker.allowWithCooldown(30 * time.Second) {
		t.Fatal("closed circuit rejected first request")
	}
	breaker.recordFailureWith(2)
	if !breaker.allowWithCooldown(30 * time.Second) {
		t.Fatal("circuit opened before reaching threshold")
	}
	breaker.recordFailureWith(2)
	if breaker.allowWithCooldown(30 * time.Second) {
		t.Fatal("open circuit allowed request before cooldown")
	}

	now = now.Add(30 * time.Second)
	if !breaker.allowWithCooldown(30 * time.Second) {
		t.Fatal("circuit did not allow half-open probe")
	}
	if breaker.allowWithCooldown(30 * time.Second) {
		t.Fatal("circuit allowed a second half-open probe")
	}
	breaker.recordSuccess()
	if !breaker.allowWithCooldown(30 * time.Second) {
		t.Fatal("successful probe did not close circuit")
	}
}

func TestCircuitBreakerFailedProbeRestartsCooldown(t *testing.T) {
	now := time.Unix(1_000, 0)
	breaker := newCircuitBreaker(func() time.Time { return now })

	breaker.recordFailureWith(1)
	now = now.Add(30 * time.Second)
	if !breaker.allowWithCooldown(30 * time.Second) {
		t.Fatal("circuit did not allow half-open probe")
	}
	breaker.recordFailureWith(1)
	if breaker.allowWithCooldown(30 * time.Second) {
		t.Fatal("failed probe did not reopen circuit")
	}
	now = now.Add(30 * time.Second)
	if !breaker.allowWithCooldown(30 * time.Second) {
		t.Fatal("reopened circuit did not start a new cooldown")
	}
}

func TestCircuitBreakerAllowsSingleConcurrentProbe(t *testing.T) {
	now := time.Unix(1_000, 0)
	breaker := newCircuitBreaker(func() time.Time { return now })
	breaker.recordFailureWith(1)
	now = now.Add(time.Second)

	var allowed atomic.Int32
	var group sync.WaitGroup
	for range 32 {
		group.Add(1)
		go func() {
			defer group.Done()
			if breaker.allowWithCooldown(time.Second) {
				allowed.Add(1)
			}
		}()
	}
	group.Wait()
	if allowed.Load() != 1 {
		t.Fatalf("allowed probes = %d, want 1", allowed.Load())
	}
}

func TestCircuitBreakerUsesExecutionPolicy(t *testing.T) {
	now := time.Unix(1_000, 0)
	breaker := newCircuitBreaker(func() time.Time { return now })

	breaker.recordFailureWith(1)
	if breaker.allowWithCooldown(30 * time.Second) {
		t.Fatal("execution threshold was not applied")
	}
	now = now.Add(time.Second)
	if !breaker.allowWithCooldown(time.Second) {
		t.Fatal("execution cooldown was not applied")
	}
}
