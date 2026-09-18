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
	now         func() time.Time
}

// breakerObservation 是熔断器提供给决策层的只读状态快照。
type breakerObservation struct {
	state          string
	probeAvailable bool
}

// newCircuitBreaker 创建使用指定时钟的目标健康状态。
func newCircuitBreaker(now func() time.Time) *circuitBreaker {
	return &circuitBreaker{now: now}
}

// allowWithCooldown 按本次执行的冷却策略判断目标能否调用。
func (breaker *circuitBreaker) allowWithCooldown(cooldown time.Duration) bool {
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	if breaker.openedAt.IsZero() {
		return true
	}
	if breaker.now().Sub(breaker.openedAt) < cooldown || breaker.probeActive {
		return false
	}
	breaker.probeActive = true
	return true
}

// observeWithCooldown 按本次执行的冷却策略读取目标健康状态。
func (breaker *circuitBreaker) observeWithCooldown(cooldown time.Duration) breakerObservation {
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	if breaker.openedAt.IsZero() {
		return breakerObservation{state: "closed"}
	}
	if breaker.now().Sub(breaker.openedAt) < cooldown {
		return breakerObservation{state: "open"}
	}
	return breakerObservation{state: "half_open", probeAvailable: !breaker.probeActive}
}

// recordSuccess 清除历史失败并关闭熔断器。
func (breaker *circuitBreaker) recordSuccess() {
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	breaker.failures = 0
	breaker.openedAt = time.Time{}
	breaker.probeActive = false
}

// recordFailureWith 按本次执行的失败阈值更新目标熔断状态。
func (breaker *circuitBreaker) recordFailureWith(threshold int) {
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	if breaker.probeActive {
		breaker.failures = threshold
		breaker.openedAt = breaker.now()
		breaker.probeActive = false
		return
	}
	breaker.failures++
	if breaker.failures >= threshold {
		breaker.openedAt = breaker.now()
	}
}

// recordNeutral 释放未形成可用性结论的 Half-Open 探测权。
func (breaker *circuitBreaker) recordNeutral() {
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	breaker.probeActive = false
}
