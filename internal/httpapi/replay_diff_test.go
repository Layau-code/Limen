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
	if len(differences) != 5 {
		t.Fatalf("differences = %+v", differences)
	}
	encoded, err := json.Marshal(differences)
	if err != nil {
		t.Fatal(err)
	}
	output := string(encoded)
	for _, expected := range []string{"effective_strategy", "candidates[0]/reason", "targets[0]", "targets[0]/mapping", "plan_hash"} {
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

func TestComparePlansShowsProviderChangeWithoutUpstreamName(t *testing.T) {
	original := decision.ExecutionPlan{
		Targets:  []decision.PlanTarget{{ModelID: "model", Target: catalog.Target{ID: "primary", Provider: "openai", UpstreamModel: "gpt-secret"}}},
		PlanHash: "sha256:original",
	}
	replay := original
	replay.Targets = []decision.PlanTarget{{ModelID: "model", Target: catalog.Target{ID: "primary", Provider: "anthropic", UpstreamModel: "claude-secret"}}}
	replay.PlanHash = "sha256:replay"
	differences := comparePlans(original, replay)
	encoded, err := json.Marshal(differences)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"path":"targets[0]"`) || strings.Contains(string(encoded), "gpt-secret") || strings.Contains(string(encoded), "claude-secret") {
		t.Fatalf("provider difference = %s", encoded)
	}
}

func TestComparePlansReportsHiddenTargetMappingChange(t *testing.T) {
	original := decision.ExecutionPlan{
		Targets: []decision.PlanTarget{{ModelID: "model", Target: catalog.Target{
			ID: "primary", Provider: "openai", UpstreamModel: "gpt-secret-v1", EndpointID: "endpoint:0123456789abcdef01234567",
		}}},
		PlanHash: "sha256:original",
	}
	replay := original
	replay.Targets = []decision.PlanTarget{{ModelID: "model", Target: catalog.Target{
		ID: "primary", Provider: "openai", UpstreamModel: "gpt-secret-v2", EndpointID: "endpoint:fedcba987654321001234567",
	}}}
	replay.PlanHash = "sha256:replay"
	differences := comparePlans(original, replay)
	encoded, err := json.Marshal(differences)
	if err != nil {
		t.Fatal(err)
	}
	output := string(encoded)
	if !strings.Contains(output, `"path":"targets[0]/mapping"`) {
		t.Fatalf("missing hidden mapping difference: %s", output)
	}
	for _, secret := range []string{"gpt-secret-v1", "gpt-secret-v2", "endpoint:0123456789abcdef01234567", "endpoint:fedcba987654321001234567"} {
		if strings.Contains(output, secret) {
			t.Fatalf("mapping difference leaked %s: %s", secret, output)
		}
	}
}
