package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/huz/limen/internal/run"
)

// PostgresStore 使用参数化事务保存受治理状态，不保存请求或响应正文。
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore 创建绑定到现有连接池的 PostgreSQL Store。
func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

// CreateRun 在指定租户下插入一个不可变配置版本的 Run。
func (store *PostgresStore) CreateRun(ctx context.Context, tenantID string, item run.Run) error {
	if store.db == nil || tenantID == "" || item.ID == "" {
		return errors.New("postgres store requires database, tenant and run")
	}
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO runs (tenant_id, id, state, soft_budget_nano_usd, settled_cost_nano_usd,
			deadline, max_parallelism, in_flight, strategy, config_version, complete_requested,
			created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		tenantID, item.ID, item.State, item.SoftBudgetNanoUSD, item.SettledCostNanoUSD,
		item.Deadline, item.MaxParallelism, item.InFlight, item.Strategy, item.ConfigVersion,
		item.CompleteRequested, item.CreatedAt, item.UpdatedAt)
	return err
}

// AdmitRequest 在 Run 行锁内完成状态检查、幂等判断和并发计数。
func (store *PostgresStore) AdmitRequest(ctx context.Context, tenantID, runID string, input AdmissionInput) (run.Request, error) {
	if store.db == nil {
		return run.Request{}, errors.New("postgres database is required")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return run.Request{}, err
	}
	defer tx.Rollback()
	var item run.Run
	if err := tx.QueryRowContext(ctx, `SELECT state, soft_budget_nano_usd, settled_cost_nano_usd, deadline, max_parallelism, in_flight FROM runs WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, runID).
		Scan(&item.State, &item.SoftBudgetNanoUSD, &item.SettledCostNanoUSD, &item.Deadline, &item.MaxParallelism, &item.InFlight); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return run.Request{}, ErrNotFound
		}
		return run.Request{}, err
	}
	var existing run.Request
	err = tx.QueryRowContext(ctx, `SELECT id, request_hash, state FROM run_requests WHERE tenant_id=$1 AND endpoint=$2 AND idempotency_key=$3`, tenantID, input.Request.Endpoint, input.Request.IdempotencyKey).
		Scan(&existing.ID, &existing.RequestHash, &existing.State)
	if err == nil {
		if existing.RequestHash != input.Request.RequestHash {
			return existing, ErrIdempotencyConflict
		}
		if isRequestInProgress(existing.State) {
			return existing, ErrRequestInProgress
		}
		return existing, ErrRequestAlreadyProcessed
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return run.Request{}, err
	}
	if err := item.Admit(input.Now); err != nil {
		return run.Request{}, err
	}
	request := input.Request
	request.TenantID, request.RunID, request.State = tenantID, runID, run.RequestAdmitted
	request.CreatedAt, request.UpdatedAt = input.Now, input.Now
	_, err = tx.ExecContext(ctx, `INSERT INTO run_requests (tenant_id,id,run_id,endpoint,idempotency_key,request_hash,state,settlement_status,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, tenantID, request.ID, runID, request.Endpoint, request.IdempotencyKey, request.RequestHash, request.State, "pending", request.CreatedAt, request.UpdatedAt)
	if err != nil {
		return run.Request{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runs SET in_flight=$3, updated_at=$4 WHERE tenant_id=$1 AND id=$2`, tenantID, runID, item.InFlight, input.Now); err != nil {
		return run.Request{}, err
	}
	if err := tx.Commit(); err != nil {
		return run.Request{}, err
	}
	return request, nil
}

// RecordAttemptStarted 在访问 Provider 前记录一次本地 Attempt。
func (store *PostgresStore) RecordAttemptStarted(ctx context.Context, tenantID string, attempt run.Attempt) error {
	if store.db == nil {
		return errors.New("postgres database is required")
	}
	_, err := store.db.ExecContext(ctx, `INSERT INTO attempts (tenant_id,id,request_id,target_id,provider,upstream_model,state,started_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, tenantID, attempt.ID, attempt.RequestID, attempt.TargetID, attempt.Provider, attempt.UpstreamModel, run.AttemptStarted, attempt.StartedAt)
	return err
}

// BeginSettlement 将执行结束的 Request 标记为待结算。
func (store *PostgresStore) BeginSettlement(ctx context.Context, tenantID, requestID string, now time.Time) (run.Request, error) {
	if store.db == nil {
		return run.Request{}, errors.New("postgres database is required")
	}
	result, err := store.db.ExecContext(ctx, `UPDATE run_requests SET state=$3, settlement_status=$4, updated_at=$5 WHERE tenant_id=$1 AND id=$2 AND state NOT IN ('settled','failed','cancelled','abandoned')`, tenantID, requestID, run.RequestSettlementPending, "pending", now)
	if err != nil {
		return run.Request{}, err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return run.Request{}, run.ErrRequestNotSettleable
	}
	return store.GetRequest(ctx, tenantID, requestID)
}

// SettleRequest 以唯一账本约束写入费用并释放 Run 并发名额。
func (store *PostgresStore) SettleRequest(ctx context.Context, tenantID, requestID string, costNanoUSD *int64, now time.Time) (run.Request, error) {
	if store.db == nil {
		return run.Request{}, errors.New("postgres database is required")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return run.Request{}, err
	}
	defer tx.Rollback()
	var request run.Request
	if err := tx.QueryRowContext(ctx, `SELECT id, run_id, state FROM run_requests WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, requestID).Scan(&request.ID, &request.RunID, &request.State); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return run.Request{}, ErrNotFound
		}
		return run.Request{}, err
	}
	if request.State == run.RequestSettled {
		return request, nil
	}
	if request.State != run.RequestSettlementPending {
		return request, run.ErrRequestNotSettleable
	}
	if costNanoUSD == nil {
		_, err = tx.ExecContext(ctx, `UPDATE run_requests SET settlement_status='pending', updated_at=$3 WHERE tenant_id=$1 AND id=$2`, tenantID, requestID, now)
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE runs SET state='suspended_accounting', in_flight=GREATEST(in_flight-1,0), updated_at=$3 WHERE tenant_id=$1 AND id=$2`, tenantID, request.RunID, now)
		}
		if err != nil {
			return request, err
		}
		if err := tx.Commit(); err != nil {
			return request, err
		}
		return request, run.ErrAccountingSuspended
	}
	var inserted bool
	if err := tx.QueryRowContext(ctx, `INSERT INTO ledger_entries (tenant_id,id,request_id,cost_nano_usd,created_at) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (tenant_id,request_id) DO NOTHING RETURNING true`, tenantID, "ledger-"+requestID, requestID, *costNanoUSD, now).Scan(&inserted); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return request, err
	}
	if inserted {
		if _, err = tx.ExecContext(ctx, `UPDATE runs SET settled_cost_nano_usd=settled_cost_nano_usd+$3, in_flight=GREATEST(in_flight-1,0), updated_at=$4 WHERE tenant_id=$1 AND id=$2`, tenantID, request.RunID, *costNanoUSD, now); err != nil {
			return request, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE run_requests SET state='settled', settlement_status='complete', ledger_recorded=true, updated_at=$3 WHERE tenant_id=$1 AND id=$2`, tenantID, requestID, now); err != nil {
		return request, err
	}
	if err := tx.Commit(); err != nil {
		return request, err
	}
	request.State = run.RequestSettled
	request.LedgerRecorded = true
	return request, nil
}

// GetRun 读取带租户条件的 Run 快照。
func (store *PostgresStore) GetRun(ctx context.Context, tenantID, runID string) (run.Run, error) {
	if store.db == nil {
		return run.Run{}, errors.New("postgres database is required")
	}
	var item run.Run
	err := store.db.QueryRowContext(ctx, `SELECT id,tenant_id,state,soft_budget_nano_usd,settled_cost_nano_usd,deadline,max_parallelism,in_flight,strategy,config_version,complete_requested,created_at,updated_at FROM runs WHERE tenant_id=$1 AND id=$2`, tenantID, runID).Scan(&item.ID, &item.TenantID, &item.State, &item.SoftBudgetNanoUSD, &item.SettledCostNanoUSD, &item.Deadline, &item.MaxParallelism, &item.InFlight, &item.Strategy, &item.ConfigVersion, &item.CompleteRequested, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return run.Run{}, ErrNotFound
	}
	return item, err
}

// GetRequest 读取不包含正文的 Request 状态。
func (store *PostgresStore) GetRequest(ctx context.Context, tenantID, requestID string) (run.Request, error) {
	if store.db == nil {
		return run.Request{}, errors.New("postgres database is required")
	}
	var item run.Request
	err := store.db.QueryRowContext(ctx, `SELECT id,tenant_id,run_id,endpoint,idempotency_key,request_hash,state,settlement_status,decision_id,ledger_recorded,lease_owner,lease_expires_at,created_at,updated_at FROM run_requests WHERE tenant_id=$1 AND id=$2`, tenantID, requestID).Scan(&item.ID, &item.TenantID, &item.RunID, &item.Endpoint, &item.IdempotencyKey, &item.RequestHash, &item.State, &item.SettlementStatus, &item.DecisionID, &item.LedgerRecorded, &item.LeaseOwner, &item.LeaseExpiresAt, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return run.Request{}, ErrNotFound
	}
	return item, err
}

func isRequestInProgress(state run.RequestState) bool {
	switch state {
	case run.RequestAdmitted, run.RequestDecisionReady, run.RequestExecuting, run.RequestSettlementPending:
		return true
	default:
		return false
	}
}

var _ Store = (*PostgresStore)(nil)
