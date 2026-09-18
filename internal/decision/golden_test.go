package decision

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

//go:generate go run ./testdata/generate.go

type goldenFixture struct {
	Name     string `json:"name"`
	Input    Input  `json:"input"`
	PlanHash string `json:"plan_hash"`
}

// TestDecisionGoldenFixtures 验证 100 组已提交快照在重建引擎后保持字节级计划和哈希一致。
func TestDecisionGoldenFixtures(t *testing.T) {
	contents, err := os.ReadFile("testdata/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []goldenFixture
	if err := json.Unmarshal(contents, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 100 {
		t.Fatalf("fixtures = %d, want 100", len(fixtures))
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			first, err := NewEngine().Decide(fixture.Input)
			if err != nil {
				t.Fatal(err)
			}
			second, err := NewEngine().Decide(fixture.Input)
			if err != nil {
				t.Fatal(err)
			}
			firstJSON, err := json.Marshal(first)
			if err != nil {
				t.Fatal(err)
			}
			secondJSON, err := json.Marshal(second)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(firstJSON, secondJSON) {
				t.Fatal("recreated engine produced different canonical plan bytes")
			}
			if first.PlanHash != fixture.PlanHash {
				t.Fatalf("plan hash = %q, want %q", first.PlanHash, fixture.PlanHash)
			}
		})
	}
}
