package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/huz/limen/internal/decision"
	"github.com/huz/limen/internal/journal"
)

// DecisionJournal 将决策输入和计划以 JSONB 持久化，不保存 Prompt 或 Response。
type DecisionJournal struct {
	db *sql.DB
}

// NewDecisionJournal 创建绑定到 PostgreSQL 连接池的决策日志。
func NewDecisionJournal(db *sql.DB) *DecisionJournal {
	return &DecisionJournal{db: db}
}

// Save 以租户和决策 ID 幂等保存决策快照。
func (store *DecisionJournal) Save(ctx context.Context, record journal.Record) error {
	if store.db == nil {
		return ErrDatabaseRequired
	}
	if err := journal.ValidateRecord(record); err != nil {
		return err
	}
	inputHash, err := decision.HashInput(record.Input)
	if err != nil {
		return err
	}
	planHash, err := decision.HashPlan(record.Plan)
	if err != nil {
		return err
	}
	if record.Plan.InputHash != inputHash || record.Plan.PlanHash != planHash {
		return errors.New("decision record hash mismatch")
	}
	inputJSON, err := json.Marshal(record.Input)
	if err != nil {
		return err
	}
	planJSON, err := json.Marshal(record.Plan)
	if err != nil {
		return err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setTenantTx(ctx, tx, record.TenantID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO decision_journal (tenant_id,decision_id,input_hash,plan_hash,input_json,plan_json,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (tenant_id,decision_id) DO NOTHING`, record.TenantID, record.ID, inputHash, planHash, inputJSON, planJSON, record.CreatedAt)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		var existingInputHash, existingPlanHash string
		if err := tx.QueryRowContext(ctx, `SELECT input_hash,plan_hash FROM decision_journal WHERE tenant_id=$1 AND decision_id=$2`, record.TenantID, record.ID).Scan(&existingInputHash, &existingPlanHash); err != nil {
			return err
		}
		if existingInputHash != inputHash || existingPlanHash != planHash {
			return journal.ErrConflict
		}
	}
	return tx.Commit()
}

// Get 读取指定租户的决策快照并重新校验哈希字段。
func (store *DecisionJournal) Get(ctx context.Context, tenantID, id string) (journal.Record, error) {
	if store.db == nil {
		return journal.Record{}, ErrDatabaseRequired
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return journal.Record{}, err
	}
	defer tx.Rollback()
	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return journal.Record{}, err
	}
	var inputJSON, planJSON []byte
	var storedInputHash, storedPlanHash string
	var record journal.Record
	if err := tx.QueryRowContext(ctx, `SELECT input_hash,plan_hash,input_json,plan_json,created_at FROM decision_journal WHERE tenant_id=$1 AND decision_id=$2`, tenantID, id).Scan(&storedInputHash, &storedPlanHash, &inputJSON, &planJSON, &record.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return journal.Record{}, journal.ErrNotFound
		}
		return journal.Record{}, err
	}
	if err := json.Unmarshal(inputJSON, &record.Input); err != nil {
		return journal.Record{}, err
	}
	if err := json.Unmarshal(planJSON, &record.Plan); err != nil {
		return journal.Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return journal.Record{}, err
	}
	record.ID, record.TenantID = id, tenantID
	if err := journal.ValidateRecord(record); err != nil {
		return journal.Record{}, err
	}
	inputHash, err := decision.HashInput(record.Input)
	if err != nil {
		return journal.Record{}, err
	}
	planHash, err := decision.HashPlan(record.Plan)
	if err != nil || storedInputHash != inputHash || storedPlanHash != planHash {
		return journal.Record{}, errors.New("decision journal hash mismatch")
	}
	return record, nil
}

var _ journal.Store = (*DecisionJournal)(nil)
