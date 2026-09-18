package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/huz/limen/internal/catalog"
	"github.com/huz/limen/internal/decision"
)

func TestComparePlansReturnsSafeStructuredDifferences(t *testing.T) {
	original := decision.ExecutionPlan{
		AlgorithmVersion:  decision.AlgorithmVersionV2,
		EffectiveStrategy: decision.StrategyBalanced,
		Candidates:        []decision.CandidateResult{{ModelID: "model", TargetID: "primary", Accepted: true, Reason: "eligible"}},
		Targets:           []decision.PlanTarget{{ModelID: "model", Target: catalog.Target{ID: "primary", UpstreamModel: "gpt-secret-upstream"}}},
		PlanHash:          "sha256:original",
	}
	replay := original
	replay.Candidates = append([]decision.CandidateResult(nil), original.Candidates...)
	replay.Targets = append([]decision.PlanTarget(nil), original.Targets...)
	replay.EffectiveStrategy = decision.StrategyEconomy
	replay.Candidates[0].Reason = "quality_tier_too_low"
	replay.Targets[0].Target.ID = "backup"
	replay.Targets[0].Target.UpstreamModel = "claude-secret-upstream"
	replay.PlanHash = "sha256:replay"

	differences := comparePlans(original, replay)
	if len(differences) != 4 {
		t.Fatalf("differences = %+v", differences)
	}
	encoded, err := json.Marshal(differences)
	if err != nil {
		t.Fatal(err)
	}
	output := string(encoded)
	for _, expected := range []string{"effective_strategy", "candidates[0]/reason", "targets[0]", "plan_hash"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing difference %q: %s", expected, output)
		}
	}
	for _, secret := range []string{"gpt-secret-upstream", "claude-secret-upstream"} {
		if strings.Contains(output, secret) {
			t.Fatalf("difference leaked upstream model: %s", output)
		}
	}
}
