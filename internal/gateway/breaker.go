package gateway

import (
	"sync"
	"time"
)

// circuitBreaker 保存单个上游目标的进程内可用性状态。
type circuitBreaker struct {
	mu          sync.Mutex
	failures    int
	openedAt    time.Time
	probeActive bool
	threshold   int
	cooldown    time.Duration
	now         func() time.Time
}

// newCircuitBreaker 创建使用指定阈值、冷却时间和时钟的熔断器。
func newCircuitBreaker(threshold int, cooldown time.Duration, now func() time.Time) *circuitBreaker {
	return &circuitBreaker{threshold: threshold, cooldown: cooldown, now: now}
}

// allow 判断目标当前能否调用，并保证 Half-Open 只放行一个探测请求。
func (breaker *circuitBreaker) allow() bool {
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	if breaker.openedAt.IsZero() {
		return true
	}
	if breaker.now().Sub(breaker.openedAt) < breaker.cooldown || breaker.probeActive {
		return false
	}
	breaker.probeActive = true
	return true
}

// recordSuccess 清除历史失败并关闭熔断器。
func (breaker *circuitBreaker) recordSuccess() {
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	breaker.failures = 0
	breaker.openedAt = time.Time{}
	breaker.probeActive = false
}

// recordFailure 累计瞬时失败，并在达到阈值或探测失败时打开熔断器。
func (breaker *circuitBreaker) recordFailure() {
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	if breaker.probeActive {
		breaker.failures = breaker.threshold
		breaker.openedAt = breaker.now()
		breaker.probeActive = false
		return
	}
	breaker.failures++
	if breaker.failures >= breaker.threshold {
		breaker.openedAt = breaker.now()
	}
}

// recordNeutral 释放未形成可用性结论的 Half-Open 探测权。
func (breaker *circuitBreaker) recordNeutral() {
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	breaker.probeActive = false
}
