package gateway

import (
	"math"

	"github.com/huz/limen/internal/cost"
	"github.com/huz/limen/internal/provider"
)

// SettlementStatus 描述一次请求的用量和成本完整程度。
type SettlementStatus string

const (
	SettlementComplete    SettlementStatus = "complete"
	SettlementPartial     SettlementStatus = "partial"
	SettlementUnavailable SettlementStatus = "unavailable"
)

// AttemptSettlement 保存一次真实上游响应的结算输入。
type AttemptSettlement struct {
	Provider      string
	UpstreamModel string
	StatusCode    int
	Pricing       *cost.Pricing
	Usage         provider.UsageRecorder
}

// SettlementSummary 是 HTTP 和日志可以安全使用的请求级结算快照。
type SettlementSummary struct {
	Status        SettlementStatus
	InputTokens   int64
	OutputTokens  int64
	TotalTokens   int64
	CostNanoUSD   int64
	CostAvailable bool
}

// Settlement 汇总一次客户端请求的所有上游响应。
type Settlement struct {
	attempts []AttemptSettlement
}

// NewSettlement 创建空的请求级结算记录。
func NewSettlement() *Settlement {
	return &Settlement{}
}

// AddAttempt 追加一次上游响应，Usage 在响应读取结束后再读取。
func (settlement *Settlement) AddAttempt(attempt AttemptSettlement) {
	settlement.attempts = append(settlement.attempts, attempt)
}

// Summary 根据当前已完成的上游读取结果计算结算快照。
func (settlement *Settlement) Summary() SettlementSummary {
	var summary SettlementSummary
	hasUsage := false
	incomplete := false
	priced := true
	for _, attempt := range settlement.attempts {
		if attempt.Usage == nil {
			incomplete = true
			continue
		}
		usage := attempt.Usage.Snapshot()
		if !usage.Complete {
			incomplete = true
			continue
		}
		hasUsage = true
		if !addTokens(&summary.InputTokens, usage.InputTokens) || !addTokens(&summary.OutputTokens, usage.OutputTokens) {
			incomplete = true
			continue
		}
		if attempt.Pricing == nil {
			priced = false
			continue
		}
		costNanoUSD, err := attempt.Pricing.Cost(usage.InputTokens, usage.OutputTokens)
		if err != nil || !addTokens(&summary.CostNanoUSD, costNanoUSD) {
			priced = false
		}
	}
	if !hasUsage {
		summary.Status = SettlementUnavailable
		return summary
	}
	if !addTokens(&summary.TotalTokens, summary.InputTokens, summary.OutputTokens) {
		incomplete = true
	}
	if incomplete || !priced {
		summary.Status = SettlementPartial
		return summary
	}
	summary.Status = SettlementComplete
	summary.CostAvailable = true
	return summary
}

func addTokens(target *int64, values ...int64) bool {
	for _, value := range values {
		if value < 0 || *target > math.MaxInt64-value {
			return false
		}
		*target += value
	}
	return true
}
