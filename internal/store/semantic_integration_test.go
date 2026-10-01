package store

import (
	"context"
	"testing"
	"time"

	"github.com/huz/limen/internal/catalog"
	"github.com/huz/limen/internal/decision"
	"github.com/huz/limen/internal/journal"
	"github.com/huz/limen/internal/semantic"
)

func TestPostgresIntegrationSemanticReplayAndShadowIsolation(t *testing.T) {
	admin, app, _ := postgresIntegrationDatabases(t)
	ctx := context.Background()
	tenantA, tenantB := integrationID("semantic-a"), integrationID("semantic-b")
	ensureIntegrationTenant(t, admin, tenantA)
	ensureIntegrationTenant(t, admin, tenantB)
	input := decision.Input{
		SchemaVersion: decision.SchemaVersionV2, AlgorithmVersion: decision.AlgorithmVersionV3, EvaluatedAtUnixMS: 1,
		Request:            decision.Request{Model: "auto"},
		SemanticAssessment: &decision.SemanticAssessment{Mode: "active", Status: "bypassed"},
		Candidates:         []decision.Candidate{{ModelID: "model", Enabled: true, SecurityAllowed: true, Target: catalog.Target{ID: "target", Provider: "openai", UpstreamModel: "test"}}},
	}
	plan, err := decision.NewEngine().Decide(input)
	if err != nil {
		t.Fatal(err)
	}
	record := journal.Record{ID: integrationID("semantic-decision"), TenantID: tenantA, Input: input, Plan: plan, CreatedAt: time.Now().UTC()}
	journalStore := NewDecisionJournal(app)
	if err := journalStore.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	stored, err := journalStore.Get(ctx, tenantA, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	engine, ok := decision.NewAlgorithmRegistry().Resolve(stored.Input.AlgorithmVersion)
	if !ok {
		t.Fatal("V3 replay algorithm missing")
	}
	replay, err := engine.Decide(stored.Input)
	if err != nil {
		t.Fatal(err)
	}
	if replay.PlanHash != plan.PlanHash || replay.InputHash != plan.InputHash {
		t.Fatal("JSONB replay changed hashes")
	}
	shadow := NewPostgresSemanticShadowStore(app)
	evaluation := semantic.ShadowEvaluation{TenantID: tenantA, DecisionID: record.ID, StateHash: "sha256:test", StateLength: 12, ModelVersion: "jev-1.13.0", TemplateVersion: "task-complexity.en.v1", MappingVersion: "task-target.v1", Language: "en", Status: "assessed", InputTokens: 10, OutputTokens: 5, CostKnown: true, CostNanoUSD: 25, CreatedAt: record.CreatedAt}
	for i := 0; i < 2; i++ {
		if err := shadow.SaveShadowEvaluation(ctx, evaluation); err != nil {
			t.Fatal(err)
		}
	}
	query := `SELECT count(*) FROM semantic_shadow_evaluations WHERE decision_id=$1`
	if count := tenantRowCount(t, app, tenantA, query, record.ID); count != 1 {
		t.Fatalf("idempotent rows = %d", count)
	}
	if count := tenantRowCount(t, app, tenantB, query, record.ID); count != 0 {
		t.Fatalf("cross-tenant rows = %d", count)
	}
	var tokens, charge int64
	if err := admin.QueryRowContext(ctx, `SELECT input_tokens,cost_nano_usd FROM semantic_shadow_evaluations WHERE tenant_id=$1 AND decision_id=$2`, tenantA, record.ID).Scan(&tokens, &charge); err != nil {
		t.Fatal(err)
	}
	if tokens != 10 || charge != 25 {
		t.Fatalf("accounting changed: %d %d", tokens, charge)
	}
	tx, err := app.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := setTenantTx(ctx, tx, tenantB); err != nil {
		t.Fatal(err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM semantic_shadow_evaluations WHERE decision_id=$1`, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if affected, _ := result.RowsAffected(); affected != 0 {
		t.Fatal("cross-tenant delete bypassed RLS")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO semantic_shadow_evaluations (tenant_id,decision_id,model_version,template_version,mapping_version,state_hash,state_length,truncated,language,status,assessment_json,created_at) VALUES ($1,$2,'forged','template','mapping','hash',1,false,'en','assessed','{}',now())`, tenantA, record.ID); err == nil {
		t.Fatal("cross-tenant write bypassed RLS")
	}
}
