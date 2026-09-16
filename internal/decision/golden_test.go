package decision

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/huz/limen/internal/catalog"
)

type goldenFixture struct {
	Name               string `json:"name"`
	MinimumQualityTier int    `json:"minimum_quality_tier"`
	DataClass          string `json:"data_class"`
	Stream             bool   `json:"stream"`
	Strategy           string `json:"strategy"`
	Target             string `json:"target"`
}

// TestDecisionGoldenFixtures 验证固定样例在重复构造引擎后保持相同计划哈希。
func TestDecisionGoldenFixtures(t *testing.T) {
	contents, err := os.ReadFile("testdata/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []goldenFixture
	if err := json.Unmarshal(contents, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) < 10 {
		t.Fatalf("fixtures = %d, want at least 10", len(fixtures))
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			strategy := fixture.Strategy
			if strategy == "" {
				strategy = StrategyBalanced
			}
			input := Input{
				SchemaVersion: SchemaVersionV1, AlgorithmVersion: AlgorithmVersionV1,
				Request: Request{Model: "auto", Stream: fixture.Stream, Contract: Contract{Active: true, MinimumQualityTier: fixture.MinimumQualityTier, DataClass: fixture.DataClass, Strategy: strategy}},
				Candidates: []Candidate{
					eligibleCandidate(catalog.Target{ID: "basic", Provider: "openai", UpstreamModel: "gpt-basic", Capabilities: []string{"text"}, SupportsStreaming: true, QualityTier: 1, CostTier: 1, ContextWindow: 10000, DataClasses: []string{"public"}}),
					eligibleCandidate(catalog.Target{ID: "balanced", Provider: "openai", UpstreamModel: "gpt-balanced", Capabilities: []string{"text"}, SupportsStreaming: true, QualityTier: 2, CostTier: 2, ContextWindow: 10000, DataClasses: []string{"public", "internal"}}),
					eligibleCandidate(catalog.Target{ID: "premium", Provider: "openai", UpstreamModel: "gpt-premium", Capabilities: []string{"text"}, SupportsStreaming: true, QualityTier: 4, CostTier: 3, ContextWindow: 10000, DataClasses: []string{"public", "internal"}}),
				},
			}
			first, err := NewEngine().Decide(input)
			if err != nil {
				t.Fatal(err)
			}
			second, err := NewEngine().Decide(input)
			if err != nil {
				t.Fatal(err)
			}
			if first.PlanHash != second.PlanHash || len(first.Targets) == 0 || first.Targets[0].Target.ID != fixture.Target {
				t.Fatalf("plan=%q/%q target=%v want=%q", first.PlanHash, second.PlanHash, first.Targets, fixture.Target)
			}
		})
	}
}
