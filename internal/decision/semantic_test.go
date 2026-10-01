package decision

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestSemanticFallbackPreservesEconomyBaseline(t *testing.T) {
	cheap := eligibleCandidate(testTargetWithCost("cheap-probe", 3, 1))
	cheap.Health = HealthSnapshot{State: "half_open", ProbeAvailable: true}
	expensive := eligibleCandidate(testTargetWithCost("expensive-healthy", 4, 5))
	baseline := decisionInput(cheap, expensive)
	baseline.AlgorithmVersion = AlgorithmVersionV2
	baseline.Request.Contract.Strategy = StrategyEconomy
	want, err := NewEngine().Decide(baseline)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"bypassed", "not_authorized", "provider_timeout", "provider_error", "low_confidence", "assessed"} {
		t.Run(status, func(t *testing.T) {
			input := baseline
			input.SchemaVersion, input.AlgorithmVersion = SchemaVersionV2, AlgorithmVersionV3
			input.SemanticAssessment = semanticTestAssessment()
			input.SemanticAssessment.Status = status
			input.SemanticAssessment.Applied = false
			input.SemanticAssessment.MinimumQualityTier = 0
			got, err := NewEngine().Decide(input)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Targets, want.Targets) {
				t.Fatalf("fallback changed baseline order: got %v want %v", got.Targets, want.Targets)
			}
		})
	}
}

func TestCanonicalSemanticInputDoesNotMutateCaller(t *testing.T) {
	input := semanticTestInput(eligibleCandidate(testTarget("z", 5, true, []string{"text"}, []string{"public"})), eligibleCandidate(testTarget("a", 5, true, []string{"text"}, []string{"public"})))
	input.SemanticAssessment.PreferredTargetIDs = []string{"z", "a", "z"}
	before, _ := json.Marshal(input)
	if _, err := CanonicalInput(input); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(input)
	if string(before) != string(after) {
		t.Fatal("canonicalization mutated caller-owned snapshot")
	}
}

func semanticTestAssessment() *SemanticAssessment {
	return &SemanticAssessment{
		Mode: "active", Status: "assessed", StateHash: "sha256:" + strings.Repeat("a", 64), StateLength: 12,
		StateBuilderVersion: "recent-user.v1", ModelVersion: "jev-1.13.0", QuestionTemplateVersion: "task-complexity.zh.v1", MappingVersion: "task-target.v1",
		Language: "zh", TaskType: "code", TaskProbabilities: map[string]float64{"extraction": 0, "transformation": 0, "writing": 0, "code": 1, "analysis": 0, "other": 0}, TaskConfidence: .95,
		Complexity: "complex", ComplexityProbabilities: map[string]float64{"simple": 0, "standard": 0, "complex": 1}, ComplexityConfidence: .95,
		ThresholdProfile: "code-complex-zh.v1", Applied: true, MinimumQualityTier: 5,
	}
}

func semanticTestInput(candidates ...Candidate) Input {
	input := decisionInput(candidates...)
	input.SchemaVersion, input.AlgorithmVersion = SchemaVersionV2, AlgorithmVersionV3
	input.Request.Contract.MinimumQualityTier = 2
	input.SemanticAssessment = semanticTestAssessment()
	return input
}

func TestSemanticQualityFallbackRestoresClientContract(t *testing.T) {
	candidate := eligibleCandidate(testTarget("quality-four", 4, true, []string{"text"}, []string{"public"}))
	plan, err := NewEngine().Decide(semanticTestInput(candidate))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Targets) != 1 || plan.Targets[0].Target.ID != "quality-four" {
		t.Fatalf("targets = %#v", plan.Targets)
	}
	if len(plan.Reasons) == 0 || plan.Reasons[len(plan.Reasons)-1] != "semantic_quality_fallback" {
		t.Fatalf("reasons = %v", plan.Reasons)
	}
	if plan.SemanticStatus != "assessed" {
		t.Fatalf("semantic status = %q", plan.SemanticStatus)
	}
}

func TestSemanticPreferenceOnlyReordersHardEligibleCandidates(t *testing.T) {
	preferred := eligibleCandidate(testTarget("preferred", 3, true, []string{"text"}, []string{"public"}))
	other := eligibleCandidate(testTarget("other", 5, true, []string{"text"}, []string{"public"}))
	assessment := semanticTestAssessment()
	assessment.MinimumQualityTier = 0
	assessment.PreferredTargetIDs = []string{"preferred"}
	input := semanticTestInput(other, preferred)
	input.SemanticAssessment = assessment
	plan, err := NewEngine().Decide(input)
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Targets[0].Target.ID; got != "preferred" {
		t.Fatalf("selected %q; want preferred", got)
	}

	input.Request.Contract.RequiredCapabilities = []string{"vision"}
	if _, err := NewEngine().Decide(input); err == nil {
		t.Fatal("semantic preference must not bypass required capabilities")
	}
}
