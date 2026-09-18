package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/huz/limen/internal/catalog"
	"github.com/huz/limen/internal/cost"
	"github.com/huz/limen/internal/decision"
)

type fixture struct {
	Name     string         `json:"name"`
	Input    decision.Input `json:"input"`
	PlanHash string         `json:"plan_hash"`
}

// main 生成固定数量的版本化决策快照及其预期计划哈希。
func main() {
	fixtures := make([]fixture, 0, 100)
	for index := 0; index < 100; index++ {
		input := fixtureInput(index)
		plan, err := decision.NewEngine().Decide(input)
		if err != nil {
			panic(err)
		}
		fixtures = append(fixtures, fixture{Name: fmt.Sprintf("replay-%03d", index), Input: input, PlanHash: plan.PlanHash})
	}
	var output bytes.Buffer
	output.WriteString("[\n")
	for index, item := range fixtures {
		encoded, err := json.Marshal(item)
		if err != nil {
			panic(err)
		}
		output.WriteString("  ")
		output.Write(encoded)
		if index+1 < len(fixtures) {
			output.WriteByte(',')
		}
		output.WriteByte('\n')
	}
	output.WriteString("]\n")
	if err := os.WriteFile("testdata/fixtures.json", output.Bytes(), 0o644); err != nil {
		panic(err)
	}
}

// fixtureInput 按稳定组合生成覆盖契约、健康、预算和排序的决策输入。
func fixtureInput(index int) decision.Input {
	dataClasses := []string{"", "public", "internal", "confidential"}
	strategies := []string{decision.StrategyBalanced, decision.StrategyEconomy}
	contract := decision.Contract{
		Active:                true,
		RequiredCapabilities:  []string{"text"},
		MinimumQualityTier:    index % 5,
		RequiredContextTokens: int64(4_000 + index%3*3_000),
		DataClass:             dataClasses[index%len(dataClasses)],
		EstimatedInputTokens:  int64(100 + index),
		EstimatedOutputTokens: int64(50 + index%17),
		Strategy:              strategies[index%len(strategies)],
	}
	run := decision.RunSnapshot{}
	if index%3 == 0 {
		run = decision.RunSnapshot{
			Governed:                true,
			SettledCostNanoUSD:      int64(index%2) * 900_000_000,
			SoftBudgetNanoUSD:       1_000_000_000,
			EconomyThresholdPercent: 20,
			RemainingDeadline:       10 * time.Second,
			MinimumAttemptWindow:    time.Second,
		}
	}
	candidates := fixtureCandidates(index)
	return decision.Input{
		SchemaVersion:     decision.SchemaVersionV1,
		AlgorithmVersion:  decision.AlgorithmVersionV1,
		ConfigVersion:     "fixture-config-v1",
		EvaluatedAtUnixMS: 1_700_000_000_000 + int64(index),
		Request:           decision.Request{Model: "auto", Stream: index%2 == 1, Contract: contract},
		Run:               run,
		Candidates:        candidates,
	}
}

// fixtureCandidates 返回始终保留至少一个可执行目标的稳定候选集。
func fixtureCandidates(index int) []decision.Candidate {
	basicHealth := decision.HealthSnapshot{State: "closed"}
	if index%11 == 0 {
		basicHealth.State = "open"
	}
	balancedHealth := decision.HealthSnapshot{State: "closed"}
	if index%13 == 0 {
		balancedHealth = decision.HealthSnapshot{State: "half_open", ProbeAvailable: false}
	}
	premiumHealth := decision.HealthSnapshot{State: "closed"}
	if index%7 == 0 {
		premiumHealth = decision.HealthSnapshot{State: "half_open", ProbeAvailable: true}
	}
	return []decision.Candidate{
		fixtureCandidate("basic", 1, 1, 8_000, false, []string{"public"}, basicHealth, cost.Pricing{InputPerMillionNanoUSD: 100_000, OutputPerMillionNanoUSD: 200_000}),
		fixtureCandidate("balanced", 2, 2, 16_000, true, []string{"public", "internal"}, balancedHealth, cost.Pricing{InputPerMillionNanoUSD: 300_000, OutputPerMillionNanoUSD: 600_000}),
		fixtureCandidate("premium", 4, 3, 32_000, true, []string{"public", "internal", "confidential"}, premiumHealth, cost.Pricing{InputPerMillionNanoUSD: 900_000, OutputPerMillionNanoUSD: 1_800_000}),
	}
}

// fixtureCandidate 构造一个不依赖运行环境的完整候选目标。
func fixtureCandidate(id string, quality, costTier int, contextWindow int64, streaming bool, dataClasses []string, health decision.HealthSnapshot, pricing cost.Pricing) decision.Candidate {
	return decision.Candidate{
		ModelID:         "smart-model",
		Enabled:         true,
		SecurityAllowed: true,
		Health:          health,
		Target: catalog.Target{
			ID:                id,
			Provider:          "openai",
			UpstreamModel:     "fixture-" + id,
			Capabilities:      []string{"text"},
			SupportsStreaming: streaming,
			QualityTier:       quality,
			CostTier:          costTier,
			ContextWindow:     contextWindow,
			DataClasses:       dataClasses,
			Pricing:           &pricing,
		},
	}
}
