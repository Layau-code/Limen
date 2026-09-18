package decision

import (
	"testing"

	"github.com/huz/limen/internal/catalog"
	"github.com/huz/limen/internal/cost"
)

func testTarget(id string, quality int, streaming bool, capabilities, dataClasses []string) catalog.Target {
	return catalog.Target{
		ID:                id,
		Provider:          "openai",
		UpstreamModel:     id,
		Capabilities:      capabilities,
		SupportsStreaming: streaming,
		QualityTier:       quality,
		CostTier:          1,
		ContextWindow:     128000,
		DataClasses:       dataClasses,
	}
}

func testTargetWithCost(id string, quality, costTier int) catalog.Target {
	target := testTarget(id, quality, true, []string{"text"}, []string{"public"})
	target.CostTier = costTier
	target.Pricing = &cost.Pricing{InputPerMillionNanoUSD: 1, OutputPerMillionNanoUSD: 1}
	return target
}

func TestDecideFiltersCapabilitiesAndSorts(t *testing.T) {
	input := Input{
		SchemaVersion:    "decision-input.v1",
		AlgorithmVersion: "decision.v1",
		Request: Request{
			Model:  "auto",
			Stream: true,
			Contract: Contract{
				Active:               true,
				RequiredCapabilities: []string{"text"},
				MinimumQualityTier:   2,
				DataClass:            "internal",
				Strategy:             "balanced",
			},
		},
		Candidates: []Candidate{
			{ModelID: "slow", Enabled: true, SecurityAllowed: true, Target: testTarget("slow", 4, true, []string{"text"}, []string{"public"})},
			{ModelID: "good", Enabled: true, SecurityAllowed: true, Target: testTarget("good", 3, true, []string{"text"}, []string{"public", "internal"})},
		},
	}
	plan, err := NewEngine().Decide(input)
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.EffectiveStrategy; got != "balanced" {
		t.Fatalf("strategy = %q", got)
	}
	if got := plan.Targets[0].Target.ID; got != "good" {
		t.Fatalf("target = %q", got)
	}
	if got := plan.Candidates[0].Reason; got != "data_policy_denied" {
		t.Fatalf("reason = %q", got)
	}
}

func TestDecideRejectsHardConstraints(t *testing.T) {
	tests := []struct {
		name   string
		input  func() Input
		reason string
	}{
		{
			name: "disabled",
			input: func() Input {
				return decisionInput(Candidate{Target: testTarget("target", 3, true, []string{"text"}, []string{"public"})})
			},
			reason: "target_disabled",
		},
		{
			name: "security policy",
			input: func() Input {
				candidate := Candidate{Enabled: true, Target: testTarget("target", 3, true, []string{"text"}, []string{"public"})}
				return decisionInput(candidate)
			},
			reason: "security_policy_denied",
		},
		{
			name: "missing capability",
			input: func() Input {
				candidate := eligibleCandidate(testTarget("target", 3, true, []string{"text"}, []string{"public"}))
				input := decisionInput(candidate)
				input.Request.Contract.RequiredCapabilities = []string{"reasoning"}
				return input
			},
			reason: "missing_capability:reasoning",
		},
		{
			name: "quality tier too low",
			input: func() Input {
				candidate := eligibleCandidate(testTarget("target", 2, true, []string{"text"}, []string{"public"}))
				input := decisionInput(candidate)
				input.Request.Contract.MinimumQualityTier = 3
				return input
			},
			reason: "quality_tier_too_low",
		},
		{
			name: "streaming unsupported",
			input: func() Input {
				candidate := eligibleCandidate(testTarget("target", 3, false, []string{"text"}, []string{"public"}))
				input := decisionInput(candidate)
				input.Request.Stream = true
				return input
			},
			reason: "streaming_unsupported",
		},
		{
			name: "context too small",
			input: func() Input {
				target := testTarget("target", 3, true, []string{"text"}, []string{"public"})
				target.ContextWindow = 10
				input := decisionInput(eligibleCandidate(target))
				input.Request.Contract.RequiredContextTokens = 11
				return input
			},
			reason: "context_window_too_small",
		},
		{
			name: "data policy",
			input: func() Input {
				candidate := eligibleCandidate(testTarget("target", 3, true, []string{"text"}, []string{"public"}))
				input := decisionInput(candidate)
				input.Request.Contract.DataClass = "internal"
				return input
			},
			reason: "data_policy_denied",
		},
		{
			name: "open circuit",
			input: func() Input {
				candidate := eligibleCandidate(testTarget("target", 3, true, []string{"text"}, []string{"public"}))
				candidate.Health.State = "open"
				return decisionInput(candidate)
			},
			reason: "circuit_open",
		},
		{
			name: "busy probe",
			input: func() Input {
				candidate := eligibleCandidate(testTarget("target", 3, true, []string{"text"}, []string{"public"}))
				candidate.Health = HealthSnapshot{State: "half_open"}
				return decisionInput(candidate)
			},
			reason: "circuit_probe_busy",
		},
		{
			name: "deadline",
			input: func() Input {
				input := decisionInput(eligibleCandidate(testTarget("target", 3, true, []string{"text"}, []string{"public"})))
				input.Run.RemainingDeadline = 100
				input.Run.MinimumAttemptWindow = 200
				return input
			},
			reason: "deadline_insufficient",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := NewEngine().Decide(test.input())
			if err == nil {
				t.Fatal("expected no eligible target error")
			}
			if got := plan.Candidates[0].Reason; got != test.reason {
				t.Fatalf("reason = %q, want %q", got, test.reason)
			}
		})
	}
}

func TestDecideNoEligibleTargetKeepsHashedEvidence(t *testing.T) {
	input := decisionInput(eligibleCandidate(testTarget("target", 1, true, []string{"text"}, []string{"public"})))
	input.Request.Contract.MinimumQualityTier = 5
	plan, err := NewEngine().Decide(input)
	if err == nil {
		t.Fatal("expected no eligible target error")
	}
	if plan.InputHash == "" || plan.PlanHash == "" {
		t.Fatalf("plan hashes = input:%q plan:%q", plan.InputHash, plan.PlanHash)
	}
	if got, hashErr := HashPlan(plan); hashErr != nil || got != plan.PlanHash {
		t.Fatalf("plan hash = %q, err=%v", got, hashErr)
	}
}

func TestDecidePreservesExplicitTargetOrderWithoutContract(t *testing.T) {
	first := eligibleCandidate(testTarget("first", 1, true, []string{"text"}, []string{"public"}))
	second := eligibleCandidate(testTarget("second", 4, true, []string{"text"}, []string{"public"}))
	input := decisionInput(first, second)
	input.Request.Model = "logical"
	input.Request.Contract.Active = false
	input.Request.Contract.RequiredCapabilities = nil
	plan, err := NewEngine().Decide(input)
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Targets[0].Target.ID; got != "first" {
		t.Fatalf("target = %q, want first", got)
	}
}

func decisionInput(candidates ...Candidate) Input {
	return Input{
		SchemaVersion:    SchemaVersionV1,
		AlgorithmVersion: AlgorithmVersionV1,
		Request: Request{
			Model: "auto",
			Contract: Contract{
				Active:               true,
				RequiredCapabilities: []string{"text"},
				DataClass:            "public",
				Strategy:             StrategyBalanced,
			},
		},
		Candidates: candidates,
	}
}

func eligibleCandidate(target catalog.Target) Candidate {
	return Candidate{Enabled: true, SecurityAllowed: true, Target: target}
}
