package config

import (
	"strings"
	"testing"
)

func TestParseSemanticRoutingRequiresOptInAndFixedRule(t *testing.T) {
	document := `{"routing":{"semantic":{"mode":"shadow","external_enabled":true,"provider":"typesafe","model_version":"jev-1.13.0","state_builder_version":"recent-user.v1","question_template_version":"task-complexity.zh.v1","mapping_version":"task-target.v1","allowed_data_classes":["public"],"sample_percent":5,"rules":[{"task_type":"code","complexity":"complex","language":"zh","minimum_task_confidence":0.8,"minimum_complexity_confidence":0.8,"minimum_probability_margin":0.15,"minimum_quality_tier":4,"preferred_target_ids":["target-a"],"threshold_profile":"code-complex-zh.v1"}]}},"models":[{"id":"smart","targets":[{"id":"target-a","provider":"openai","upstream_model":"gpt-test","quality_tier":4,"cost_tier":1,"context_window":4096,"data_classes":["public"]}]}]}`
	_, routing, _, err := ParseModels([]byte(document), DefaultRouting())
	if err != nil {
		t.Fatal(err)
	}
	if routing.Semantic.Mode != "shadow" || !routing.Semantic.ExternalEnabled || len(routing.Semantic.Rules) != 1 {
		t.Fatalf("semantic routing = %#v", routing.Semantic)
	}

	active := strings.Replace(document, `"mode":"shadow"`, `"mode":"active"`, 1)
	if _, _, _, err := ParseModels([]byte(active), DefaultRouting()); err == nil || !strings.Contains(err.Error(), "evaluation_report") {
		t.Fatalf("active rule without evaluation report: %v", err)
	}
}

func TestParseOldModelDocumentDefaultsSemanticOff(t *testing.T) {
	document := `{"models":[{"id":"smart","targets":[{"id":"target-a","provider":"openai","upstream_model":"gpt-test"}]}]}`
	_, routing, _, err := ParseModels([]byte(document), DefaultRouting())
	if err != nil {
		t.Fatal(err)
	}
	if routing.Semantic.Mode != "off" || routing.Semantic.ExternalEnabled {
		t.Fatalf("semantic default = %#v", routing.Semantic)
	}
}
