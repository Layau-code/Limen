package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huz/limen/internal/provider"
)

func TestHealthURLNormalizesListenAddresses(t *testing.T) {
	tests := map[string]string{
		":8080":          "http://127.0.0.1:8080/readyz",
		"0.0.0.0:8080":   "http://127.0.0.1:8080/readyz",
		"[::]:8080":      "http://127.0.0.1:8080/readyz",
		"127.0.0.1:9090": "http://127.0.0.1:9090/readyz",
	}
	for addr, want := range tests {
		got, err := healthURL("", addr)
		if err != nil || got != want {
			t.Fatalf("addr %q => %q, %v; want %q", addr, got, err, want)
		}
	}
}

func TestHealthcheckUsesExplicitURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err := runHealthcheck(context.Background(), server.URL+"/readyz", "bad-address", server.Client()); err != nil {
		t.Fatal(err)
	}
}

func TestRunCommandVersionDoesNotLoadConfiguration(t *testing.T) {
	var stdout, stderr strings.Builder
	if code, handled := runCommand([]string{"version"}, &stdout, &stderr); !handled || code != 0 {
		t.Fatalf("code=%d handled=%t stderr=%q", code, handled, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"version":"dev"`) || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunCommandDemoReportsFallbackAndDraftImpact(t *testing.T) {
	var stdout, stderr strings.Builder
	if code, handled := runCommand([]string{"demo"}, &stdout, &stderr); !handled || code != 0 {
		t.Fatalf("code=%d handled=%t stderr=%q", code, handled, stderr.String())
	}
	var result struct {
		Scenario       string `json:"scenario"`
		Route          string `json:"route"`
		Attempts       int    `json:"attempts"`
		DraftProvider  string `json:"draft_provider"`
		ImpactDetected bool   `json:"impact_detected"`
		ProviderCalls  int    `json:"provider_calls"`
		Selection      struct {
			RequestedModel string   `json:"requested_model"`
			SelectedModel  string   `json:"selected_model"`
			DataClass      string   `json:"data_class"`
			MinimumQuality int      `json:"minimum_quality_tier"`
			Rejected       []string `json:"rejected"`
		} `json:"selection"`
		Run struct {
			State              string `json:"state"`
			SettlementStatus   string `json:"settlement_status"`
			InFlight           int    `json:"in_flight"`
			SettledCostNanoUSD int64  `json:"settled_cost_nano_usd"`
		} `json:"run"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &result); err != nil {
		t.Fatalf("demo output = %q: %v", stdout.String(), err)
	}
	if result.Scenario != "draft-impact" || result.Route != "openai:503>anthropic:200" || result.Attempts != 2 || result.DraftProvider != "anthropic" || !result.ImpactDetected || result.ProviderCalls != 2 {
		t.Fatalf("demo result = %+v", result)
	}
	if result.Selection.RequestedModel != "auto" || result.Selection.SelectedModel != "smart-model" || result.Selection.DataClass != "internal" || result.Selection.MinimumQuality != 3 {
		t.Fatalf("demo selection = %+v", result.Selection)
	}
	if len(result.Selection.Rejected) != 1 || result.Selection.Rejected[0] != "basic-model:quality_tier_too_low" {
		t.Fatalf("demo rejected candidates = %v", result.Selection.Rejected)
	}
	if result.Run.State != "active" || result.Run.SettlementStatus != "complete" || result.Run.InFlight != 0 || result.Run.SettledCostNanoUSD != 250000 {
		t.Fatalf("demo run = %+v", result.Run)
	}
	if strings.Contains(stdout.String(), "gpt-") || strings.Contains(stdout.String(), "claude-") {
		t.Fatalf("demo leaked upstream model: %s", stdout.String())
	}
}

// TestRunCommandValidateModelsWithoutSecrets 验证配置预检不读取密钥或访问 Provider。
func TestRunCommandValidateModelsWithoutSecrets(t *testing.T) {
	t.Setenv("LIMEN_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	directory := t.TempDir()
	modelsPath := filepath.Join(directory, "models.json")
	if err := os.WriteFile(modelsPath, []byte(`{
  "models": [{
    "id": "smart-model",
    "targets": [{"provider":"openai","upstream_model":"gpt-secret-model"}]
  }]
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	if code, handled := runCommand([]string{"validate", "--models", modelsPath}, &stdout, &stderr); !handled || code != 0 {
		t.Fatalf("code=%d handled=%t stderr=%q", code, handled, stderr.String())
	}
	var result struct {
		ConfigVersion string         `json:"config_version"`
		Models        int            `json:"models"`
		Targets       int            `json:"targets"`
		Providers     map[string]int `json:"providers"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &result); err != nil {
		t.Fatalf("validate output = %q: %v", stdout.String(), err)
	}
	if result.ConfigVersion == "" || result.Models != 1 || result.Targets != 1 || result.Providers["openai"] != 1 {
		t.Fatalf("validate result = %+v", result)
	}
	if strings.Contains(stdout.String(), "gpt-secret-model") || stderr.Len() != 0 {
		t.Fatalf("validate leaked data or wrote stderr: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// TestRunCommandValidateRequiresModelsFile 返回清晰的命令参数错误。
func TestRunCommandValidateRequiresModelsFile(t *testing.T) {
	var stdout, stderr strings.Builder
	if code, handled := runCommand([]string{"validate"}, &stdout, &stderr); !handled || code != 1 {
		t.Fatalf("code=%d handled=%t stdout=%q stderr=%q", code, handled, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "validate 需要 --models") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

// TestRunCommandValidateRejectsEndpointMismatch 预检阶段拒绝无法绑定进程 endpoint 的目标。
func TestRunCommandValidateRejectsEndpointMismatch(t *testing.T) {
	endpointID, err := provider.EndpointIDForBaseURL("https://provider.example/v1")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	modelsPath := filepath.Join(directory, "models.json")
	contents := fmt.Sprintf(`{
  "models": [{"id":"smart-model","targets":[{"provider":"openai","upstream_model":"fixture","endpoint_id":%q}] }]
}`, endpointID)
	if err := os.WriteFile(modelsPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_BASE_URL", "https://other.example/v1")

	var stdout, stderr strings.Builder
	if code, handled := runCommand([]string{"validate", "--models", modelsPath}, &stdout, &stderr); !handled || code != 1 {
		t.Fatalf("code=%d handled=%t stdout=%q stderr=%q", code, handled, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "endpoint 绑定") || strings.Contains(stderr.String(), endpointID) {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

// TestRunCommandDiffReportsSafeConfigImpact 验证离线 diff 只输出结构变化和 opaque 目标引用。
func TestRunCommandDiffReportsSafeConfigImpact(t *testing.T) {
	directory := t.TempDir()
	basePath := filepath.Join(directory, "base.json")
	candidatePath := filepath.Join(directory, "candidate.json")
	base := []byte(`{"models":[{"id":"smart","targets":[{"id":"target","provider":"openai","upstream_model":"gpt-secret-old"}]}]}`)
	candidate := []byte(`{"models":[{"id":"smart","targets":[{"id":"target","provider":"anthropic","upstream_model":"claude-secret-new"}]}]}`)
	if err := os.WriteFile(basePath, base, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidatePath, candidate, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	if code, handled := runCommand([]string{"diff", "--base", basePath, "--candidate", candidatePath}, &stdout, &stderr); !handled || code != 0 {
		t.Fatalf("code=%d handled=%t stdout=%q stderr=%q", code, handled, stdout.String(), stderr.String())
	}
	var result struct {
		BaseVersion      string `json:"base_version"`
		CandidateVersion string `json:"candidate_version"`
		Changed          bool   `json:"changed"`
		Changes          []struct {
			Path string `json:"path"`
			Kind string `json:"kind"`
		} `json:"changes"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &result); err != nil {
		t.Fatalf("diff output = %q: %v", stdout.String(), err)
	}
	if result.BaseVersion == "" || result.CandidateVersion == "" || !result.Changed || len(result.Changes) != 2 {
		t.Fatalf("diff result = %+v", result)
	}
	if !strings.Contains(result.Changes[0].Path, "target-") || result.Changes[0].Kind != "changed" {
		t.Fatalf("diff changes = %+v", result.Changes)
	}
	if strings.Contains(stdout.String(), "gpt-secret-old") || strings.Contains(stdout.String(), "claude-secret-new") || stderr.Len() != 0 {
		t.Fatalf("diff leaked data or wrote stderr: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// TestRunCommandDiffFailOnBlocksProviderChange 验证发布门禁能阻止高风险映射变化且只输出安全摘要。
func TestRunCommandDiffFailOnBlocksProviderChange(t *testing.T) {
	directory := t.TempDir()
	basePath := filepath.Join(directory, "base.json")
	candidatePath := filepath.Join(directory, "candidate.json")
	if err := os.WriteFile(basePath, []byte(`{"models":[{"id":"smart","targets":[{"id":"target","provider":"openai","upstream_model":"gpt-secret-old"}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidatePath, []byte(`{"models":[{"id":"smart","targets":[{"id":"target","provider":"anthropic","upstream_model":"claude-secret-new"}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	args := []string{"diff", "--base", basePath, "--candidate", candidatePath, "--fail-on", "provider"}
	if code, handled := runCommand(args, &stdout, &stderr); !handled || code != 1 {
		t.Fatalf("code=%d handled=%t stdout=%q stderr=%q", code, handled, stdout.String(), stderr.String())
	}
	var result struct {
		Blocked    bool `json:"blocked"`
		Violations []struct {
			Category string `json:"category"`
			Path     string `json:"path"`
			Kind     string `json:"kind"`
		} `json:"violations"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &result); err != nil {
		t.Fatalf("diff output = %q: %v", stdout.String(), err)
	}
	if !result.Blocked || len(result.Violations) != 2 || result.Violations[0].Category != "provider" || result.Violations[0].Kind != "changed" || !strings.Contains(result.Violations[0].Path, "target-") {
		t.Fatalf("diff gate result = %+v", result)
	}
	if !strings.Contains(stderr.String(), "diff 被策略阻止") || strings.Contains(stdout.String(), "gpt-secret-old") || strings.Contains(stdout.String(), "claude-secret-new") {
		t.Fatalf("gate leaked data or missing error: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// TestRunCommandDiffRejectsUnknownFailOnCategory 验证未知门禁类别不会输出配置摘要。
func TestRunCommandDiffRejectsUnknownFailOnCategory(t *testing.T) {
	directory := t.TempDir()
	basePath := filepath.Join(directory, "base.json")
	if err := os.WriteFile(basePath, []byte(`{"models":[{"id":"model","targets":[{"id":"target","provider":"openai","upstream_model":"gpt-test"}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	args := []string{"diff", "--base", basePath, "--candidate", basePath, "--fail-on", "secret"}
	if code, handled := runCommand(args, &stdout, &stderr); !handled || code != 1 {
		t.Fatalf("code=%d handled=%t stdout=%q stderr=%q", code, handled, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "不支持的类别") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunCommandExplainIsDeterministicAndHidesPrompt(t *testing.T) {
	directory := t.TempDir()
	modelsPath := filepath.Join(directory, "models.json")
	requestPath := filepath.Join(directory, "request.json")
	if err := os.WriteFile(modelsPath, []byte(`{
  "models": [{
    "id": "smart-model",
    "targets": [
      {"id":"basic","provider":"openai","upstream_model":"fixture-secret-basic","quality_tier":1,"context_window":4000,"data_classes":["public"]},
      {"id":"premium","provider":"anthropic","upstream_model":"fixture-secret-premium","quality_tier":4,"context_window":16000,"data_classes":["public","internal"]}
    ]
  }]
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requestPath, []byte(`{
  "model":"auto",
  "messages":[{"role":"user","content":"sensitive prompt"}],
  "limen":{"required_capabilities":["text"],"minimum_quality_tier":2,"data_class":"internal","strategy":"economy"}
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var first, second, stderr strings.Builder
	args := []string{"explain", "--models", modelsPath, "--request", requestPath}
	if code, handled := runCommand(args, &first, &stderr); !handled || code != 0 {
		t.Fatalf("first explain code=%d handled=%t stderr=%q", code, handled, stderr.String())
	}
	if code, handled := runCommand(args, &second, &stderr); !handled || code != 0 {
		t.Fatalf("second explain code=%d handled=%t stderr=%q", code, handled, stderr.String())
	}
	if first.String() != second.String() {
		t.Fatalf("explain output changed:\nfirst=%ssecond=%s", first.String(), second.String())
	}
	var result struct {
		EffectiveStrategy string `json:"effective_strategy"`
		PlanHash          string `json:"plan_hash"`
		Candidates        []struct {
			Accepted bool   `json:"accepted"`
			Reason   string `json:"reason"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(first.String()), &result); err != nil {
		t.Fatalf("explain output = %q: %v", first.String(), err)
	}
	if result.EffectiveStrategy != "economy" || result.PlanHash == "" || len(result.Candidates) != 2 || result.Candidates[0].Accepted || result.Candidates[0].Reason != "quality_tier_too_low" || !result.Candidates[1].Accepted {
		t.Fatalf("explain result = %+v", result)
	}
	if strings.Contains(first.String(), "sensitive prompt") || strings.Contains(first.String(), "fixture-secret") {
		t.Fatalf("explain output leaked sensitive data: %s", first.String())
	}
}

func TestRunCommandExplainReportsNoEligibleTarget(t *testing.T) {
	directory := t.TempDir()
	modelsPath := filepath.Join(directory, "models.json")
	requestPath := filepath.Join(directory, "request.json")
	if err := os.WriteFile(modelsPath, []byte(`{"models":[{"id":"model","targets":[{"id":"basic","provider":"openai","upstream_model":"fixture","quality_tier":1}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requestPath, []byte(`{"model":"auto","messages":[{"role":"user","content":"hello"}],"limen":{"minimum_quality_tier":5}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	if code, handled := runCommand([]string{"explain", "--models", modelsPath, "--request", requestPath}, &stdout, &stderr); !handled || code != 0 {
		t.Fatalf("code=%d handled=%t stderr=%q", code, handled, stderr.String())
	}
	var result struct {
		DecisionError string `json:"decision_error"`
		Targets       []any  `json:"targets"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &result); err != nil {
		t.Fatal(err)
	}
	if result.DecisionError != "no_eligible_target" || len(result.Targets) != 0 {
		t.Fatalf("result = %+v", result)
	}
}
