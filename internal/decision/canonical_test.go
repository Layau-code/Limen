package decision

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestDecisionHashesAreStable(t *testing.T) {
	input := Input{
		SchemaVersion:    SchemaVersionV1,
		AlgorithmVersion: AlgorithmVersionV2,
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

func TestDecisionCanonicalizesUnorderedInput(t *testing.T) {
	first := canonicalTestInput([]string{"text", "json"}, []string{"a", "b"})
	second := canonicalTestInput([]string{"json", "text"}, []string{"a", "b"})

	firstHash, err := HashInput(first)
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := HashInput(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash {
		t.Fatalf("semantic input hashes differ: %q != %q", firstHash, secondHash)
	}

	firstPlan, err := NewEngine().Decide(first)
	if err != nil {
		t.Fatal(err)
	}
	secondPlan, err := NewEngine().Decide(second)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := json.Marshal(firstPlan)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(secondPlan)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("semantic plans differ:\n%s\n%s", firstJSON, secondJSON)
	}
}

func canonicalTestInput(required []string, targetIDs []string) Input {
	return Input{
		SchemaVersion:    SchemaVersionV1,
		AlgorithmVersion: AlgorithmVersionV2,
		Request:          Request{Model: "auto", Contract: Contract{Active: true, RequiredCapabilities: required}},
		Candidates: []Candidate{
			{ModelID: "model", Enabled: true, SecurityAllowed: true, Target: testTarget(targetIDs[0], 2, true, []string{"json", "text"}, []string{"internal", "public"})},
			{ModelID: "model", Enabled: true, SecurityAllowed: true, Target: testTarget(targetIDs[1], 2, true, []string{"text", "json"}, []string{"public", "internal"})},
		},
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
