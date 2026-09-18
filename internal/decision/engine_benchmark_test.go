package decision

import (
	"fmt"
	"testing"

	"github.com/huz/limen/internal/catalog"
	"github.com/huz/limen/internal/cost"
)

// TestDecisionBenchmarkInputCoversHundredCandidates 确保性能基准覆盖最终设计规定的候选规模。
func TestDecisionBenchmarkInputCoversHundredCandidates(t *testing.T) {
	input := decisionBenchmarkInput(100)
	if len(input.Candidates) != 100 {
		t.Fatalf("candidate count = %d, want 100", len(input.Candidates))
	}
}

// BenchmarkDecisionEngine100Candidates 测量包含哈希和排序的纯决策路径。
func BenchmarkDecisionEngine100Candidates(b *testing.B) {
	input := decisionBenchmarkInput(100)
	engine := NewEngine()
	plan, err := engine.Decide(input)
	if err != nil {
		b.Fatal(err)
	}
	if len(plan.Targets) != len(input.Candidates) {
		b.Fatalf("target count = %d, want %d", len(plan.Targets), len(input.Candidates))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := engine.Decide(input); err != nil {
			b.Fatal(err)
		}
	}
}

// decisionBenchmarkInput 构造固定的多目标输入，避免基准依赖网络或当前配置。
func decisionBenchmarkInput(count int) Input {
	input := Input{
		SchemaVersion:     SchemaVersionV1,
		AlgorithmVersion:  AlgorithmVersionV2,
		EvaluatedAtUnixMS: 1,
		Request: Request{
			Model:  "auto",
			Stream: true,
			Contract: Contract{
				Active:                true,
				RequiredCapabilities:  []string{"text"},
				RequiredContextTokens: 32000,
				DataClass:             "internal",
				EstimatedInputTokens:  4000,
				EstimatedOutputTokens: 1200,
				Strategy:              StrategyBalanced,
			},
		},
		Candidates: make([]Candidate, count),
	}
	for index := range input.Candidates {
		input.Candidates[index] = Candidate{
			ModelID:         "model-" + fmt.Sprintf("%03d", index),
			Enabled:         true,
			SecurityAllowed: true,
			Health:          HealthSnapshot{State: "closed"},
			Target: catalog.Target{
				ID:                "target-" + fmt.Sprintf("%03d", index),
				Provider:          "openai",
				UpstreamModel:     "fixture-model",
				Capabilities:      []string{"text"},
				SupportsStreaming: true,
				QualityTier:       index%5 + 1,
				CostTier:          index%3 + 1,
				ContextWindow:     128000,
				DataClasses:       []string{"public", "internal"},
				Pricing:           &cost.Pricing{InputPerMillionNanoUSD: 250000000, OutputPerMillionNanoUSD: 2000000000},
			},
		}
	}
	return input
}
