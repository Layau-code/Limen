package semantic

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/huz/limen/internal/config"
	"github.com/huz/limen/internal/provider"
)

func TestDecodeJevRejectsMissingAndNullNumbers(t *testing.T) {
	const valid = `{"model":"jev-1.13.0","answers":{"task_type":{"type":"choice","choice":"code","confidence":0.9,"probabilities":{"extraction":0.02,"transformation":0.02,"writing":0.02,"code":0.9,"analysis":0.02,"other":0.02}},"complexity":{"type":"score","score":1.8,"confidence":0.85,"probabilities":{"0":0.05,"1":0.1,"2":0.85}}},"usage":{"input_tokens":24,"output_tokens":9}}`
	for _, path := range [][]string{{"answers", "task_type", "confidence"}, {"answers", "complexity", "confidence"}, {"answers", "complexity", "score"}, {"usage", "input_tokens"}, {"usage", "output_tokens"}} {
		for _, missing := range []bool{true, false} {
			t.Run(strings.Join(path, "/")+map[bool]string{true: "/missing", false: "/null"}[missing], func(t *testing.T) {
				var object map[string]any
				if err := json.Unmarshal([]byte(valid), &object); err != nil {
					t.Fatal(err)
				}
				field := object
				for _, name := range path[:len(path)-1] {
					field = field[name].(map[string]any)
				}
				if missing {
					delete(field, path[len(path)-1])
				} else {
					field[path[len(path)-1]] = nil
				}
				body, _ := json.Marshal(object)
				if _, err := decodeJevResponse(body, "jev-1.13.0"); err == nil {
					t.Fatal("incomplete result accepted")
				}
			})
		}
	}
	for _, body := range []string{strings.Replace(valid, `"extraction":0.02`, `"extraction":null`, 1), strings.Replace(valid, `"choice":"code"`, `"choice":"writing"`, 1)} {
		if _, err := decodeJevResponse([]byte(body), "jev-1.13.0"); err == nil {
			t.Fatal("inconsistent classification accepted")
		}
	}
}

func TestOtherTaskNeverChangesRouting(t *testing.T) {
	cfg := config.SemanticRouting{Rules: []config.SemanticRule{{TaskType: "other", Complexity: "simple", MinimumQualityTier: 5}}}
	result := Assessment{TaskType: "other", Complexity: "simple", TaskConfidence: 1, ComplexityConfidence: 1, TaskProbabilities: map[string]float64{"other": 1}, ComplexityProbabilities: map[string]float64{"simple": 1}}
	if Map(cfg, State{Language: "en"}, result).Applied {
		t.Fatal("other classification must use baseline")
	}
}

func TestBuildStateEmptyLatestUserDoesNotAssessPreviousAnswer(t *testing.T) {
	if _, ok := BuildState([]provider.Message{{Role: "assistant", Content: "stale answer"}, {Role: "user", Content: "  "}}); ok {
		t.Fatal("empty latest user must skip assessment")
	}
}

func TestBuildStateBoundsHistoryAndUnicode(t *testing.T) {
	state, ok := BuildState([]provider.Message{
		{Role: "system", Content: "private policy"},
		{Role: "user", Content: "old"},
		{Role: "assistant", Content: "older answer"},
		{Role: "user", Content: "second"},
		{Role: "assistant", Content: "context"},
		{Role: "user", Content: "latest"},
	})
	if !ok || state.Text != "user: old\nassistant: older answer\nuser: second\nassistant: context\nuser: latest" {
		t.Fatalf("state = %#v, usable = %v", state, ok)
	}
	if strings.Contains(state.Text, "private policy") || state.Truncated || state.Hash == "" {
		t.Fatalf("state metadata = %#v", state)
	}

	longButUsable := strings.Repeat("汉", 1365) // 4,095 UTF-8 bytes before the role prefix.
	state, ok = BuildState([]provider.Message{{Role: "user", Content: longButUsable}})
	if !ok || !state.Truncated || len(state.Text) > maxStateBytes || !utf8.ValidString(state.Text) {
		t.Fatalf("bounded Unicode state = usable %v, len %d, truncated %v", ok, len(state.Text), state.Truncated)
	}
	if _, ok := BuildState([]provider.Message{{Role: "user", Content: strings.Repeat("x", maxStateBytes+1)}}); ok {
		t.Fatal("oversized latest user message must not be assessed")
	}
}

func TestDecodeJevResponseValidatesEnumsDistributionAndVersion(t *testing.T) {
	body := []byte(`{"model":"jev-1.13.0","answers":{"task_type":{"type":"choice","choice":"code","confidence":0.9,"probabilities":{"extraction":0.02,"transformation":0.02,"writing":0.02,"code":0.9,"analysis":0.02,"other":0.02}},"complexity":{"type":"score","score":1.4,"confidence":0.85,"probabilities":{"0":0.05,"1":0.1,"2":0.85}}},"usage":{"input_tokens":24,"output_tokens":9}}`)
	result, err := decodeJevResponse(body, "jev-1.13.0")
	if err != nil || result.TaskType != "code" || result.Complexity != "complex" || result.InputTokens != 24 || result.OutputTokens != 9 {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	_, err = decodeJevResponse(body, "jev-1.12.0")
	if failure, ok := err.(*Failure); !ok || !failure.VersionMismatch {
		t.Fatalf("version mismatch error = %#v", err)
	}
	invalid := strings.Replace(string(body), `"code":0.9`, `"code":0.3`, 1)
	if _, err := decodeJevResponse([]byte(invalid), "jev-1.13.0"); err == nil {
		t.Fatal("invalid probability distribution must be rejected")
	}
}

func TestMapAppliesOnlyConfidentNonTruncatedSignals(t *testing.T) {
	cfg := config.SemanticRouting{
		Mode: "active", MappingVersion: "task-target.v1",
		Rules: []config.SemanticRule{{TaskType: "code", Complexity: "complex", Language: "zh", MinimumTaskConfidence: .8, MinimumComplexityConfidence: .8, MinimumProbabilityMargin: .2, MinimumQualityTier: 4, PreferredTargetIDs: []string{"target-a"}, ThresholdProfile: "code-complex-zh.v1"}},
	}
	state := State{Hash: "sha256:abc", Length: 32, Language: "zh"}
	result := Assessment{ModelVersion: "jev-1.13.0", TaskType: "code", TaskConfidence: .91, TaskProbabilities: map[string]float64{"code": .9, "other": .1}, Complexity: "complex", ComplexityConfidence: .88, ComplexityProbabilities: map[string]float64{"simple": .05, "standard": .1, "complex": .85}}
	assessment := Map(cfg, state, result)
	if assessment.Status != "assessed" || !assessment.Applied || assessment.MinimumQualityTier != 4 || len(assessment.PreferredTargetIDs) != 1 {
		t.Fatalf("assessment = %#v", assessment)
	}
	result.TaskConfidence = .6
	assessment = Map(cfg, state, result)
	if assessment.Status != "low_confidence" || assessment.Applied {
		t.Fatalf("low-confidence assessment = %#v", assessment)
	}
	state.Truncated = true
	result.TaskConfidence = .95
	assessment = Map(cfg, state, result)
	if assessment.Applied {
		t.Fatalf("truncated state must not affect routing: %#v", assessment)
	}
}
