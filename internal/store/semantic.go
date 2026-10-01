package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/huz/limen/internal/semantic"
)

// PostgresSemanticShadowStore persists prompt-free asynchronous assessment metadata.
type PostgresSemanticShadowStore struct{ db *sql.DB }

// NewPostgresSemanticShadowStore 创建绑定到 PostgreSQL 的影子存储。
func NewPostgresSemanticShadowStore(db *sql.DB) *PostgresSemanticShadowStore {
	return &PostgresSemanticShadowStore{db: db}
}

// SaveShadowEvaluation 在租户上下文中写入不含原始状态文本的影子结果。
func (store *PostgresSemanticShadowStore) SaveShadowEvaluation(ctx context.Context, evaluation semantic.ShadowEvaluation) error {
	if store == nil || store.db == nil {
		return ErrDatabaseRequired
	}
	if evaluation.TenantID == "" || evaluation.DecisionID == "" || evaluation.StateHash == "" || evaluation.StateLength < 0 {
		return errors.New("invalid semantic shadow evaluation")
	}
	assessment, err := json.Marshal(map[string]any{
		"task_type": evaluation.TaskType, "task_probabilities": evaluation.TaskProbabilities, "task_confidence": evaluation.TaskConfidence,
		"complexity": evaluation.Complexity, "complexity_probabilities": evaluation.ComplexityProbabilities, "complexity_confidence": evaluation.ComplexityConfidence,
	})
	if err != nil {
		return err
	}
	targets, err := json.Marshal(evaluation.CounterfactualTargets)
	if err != nil {
		return err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setTenantTx(ctx, tx, evaluation.TenantID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO semantic_shadow_evaluations
		(tenant_id,decision_id,model_version,template_version,mapping_version,state_hash,state_length,truncated,language,status,reason,assessment_json,counterfactual_targets,counterfactual_plan_hash,input_tokens,output_tokens,cost_nano_usd,cost_known,duration_ms,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
		ON CONFLICT (tenant_id,decision_id,model_version,template_version,mapping_version) DO NOTHING`,
		evaluation.TenantID, evaluation.DecisionID, evaluation.ModelVersion, evaluation.TemplateVersion, evaluation.MappingVersion, evaluation.StateHash, evaluation.StateLength, evaluation.Truncated, evaluation.Language, evaluation.Status, evaluation.Reason, assessment, targets, evaluation.CounterfactualPlanHash, evaluation.InputTokens, evaluation.OutputTokens, evaluation.CostNanoUSD, evaluation.CostKnown, evaluation.Duration.Milliseconds(), evaluation.CreatedAt)
	if err != nil {
		return err
	}
	return tx.Commit()
}

var _ semantic.ShadowStore = (*PostgresSemanticShadowStore)(nil)
