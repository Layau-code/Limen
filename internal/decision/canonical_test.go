package decision

import (
	"testing"
)

func TestDecisionHashesAreStable(t *testing.T) {
	input := Input{
		SchemaVersion:    SchemaVersionV1,
		AlgorithmVersion: AlgorithmVersionV1,
		Request: Request{
			Model: "auto",
			Contract: Contract{
				Active:               true,
				RequiredCapabilities: []string{"text"},
				Strategy:             StrategyBalanced,
			},
		},
		Candidates: []Candidate{{
			ModelID:         "model",
			Enabled:         true,
			SecurityAllowed: true,
			Target:          testTarget("target", 2, true, []string{"text"}, []string{"public"}),
		}},
	}
	first, err := HashInput(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashInput(input)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("input hashes differ: %q != %q", first, second)
	}
	plan, err := NewEngine().Decide(input)
	if err != nil {
		t.Fatal(err)
	}
	planHash, err := HashPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if planHash != plan.PlanHash {
		t.Fatalf("plan hash = %q, want %q", plan.PlanHash, planHash)
	}
}

func TestDecideUsesEconomyStrategyNearSoftBudget(t *testing.T) {
	input := Input{
		SchemaVersion:    SchemaVersionV1,
		AlgorithmVersion: AlgorithmVersionV1,
		Request: Request{
			Model: "auto",
			Contract: Contract{
				Active:               true,
				RequiredCapabilities: []string{"text"},
				Strategy:             StrategyBalanced,
			},
		},
		Run: RunSnapshot{
			Governed:                true,
			SettledCostNanoUSD:      85,
			SoftBudgetNanoUSD:       100,
			EconomyThresholdPercent: 20,
		},
		Candidates: []Candidate{
			{ModelID: "expensive", Enabled: true, SecurityAllowed: true, Target: testTargetWithCost("expensive", 3, 3)},
			{ModelID: "cheap", Enabled: true, SecurityAllowed: true, Target: testTargetWithCost("cheap", 2, 1)},
		},
	}
	plan, err := NewEngine().Decide(input)
	if err != nil {
		t.Fatal(err)
	}
	if plan.EffectiveStrategy != StrategyEconomy || len(plan.Reasons) != 1 || plan.Reasons[0] != "economy_threshold_reached" {
		t.Fatalf("strategy = %q reasons = %v", plan.EffectiveStrategy, plan.Reasons)
	}
	if got := plan.Targets[0].Target.ID; got != "cheap" {
		t.Fatalf("target = %q, want cheap", got)
	}
}
