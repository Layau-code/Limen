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
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return err
	}
	if err := insertRunTx(ctx, tx, tenantID, item); err != nil {
		return err
	}
	return tx.Commit()
}

// CreateRunWithMutation 幂等地创建控制面 Run，并保存原始操作哈希。
func (store *PostgresStore) CreateRunWithMutation(ctx context.Context, tenantID string, item run.Run, mutation run.Mutation) (run.Run, error) {
	if store.db == nil {
		return run.Run{}, errors.New("postgres database is required")
	}
	if mutation.Key == "" || mutation.Hash == "" {
		return run.Run{}, run.ErrIdempotencyKeyRequired
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return run.Run{}, err
	}
	defer tx.Rollback()
	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return run.Run{}, err
	}
	resourceID, existingHash, err := findOperationTx(ctx, tx, tenantID, "POST /v1/limen/runs", mutation.Key)
	if err == nil {
		if existingHash != mutation.Hash {
			return run.Run{}, run.ErrIdempotencyConflict
		}
		return getRunTx(ctx, tx, tenantID, resourceID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return run.Run{}, err
	}
	if err := insertRunTx(ctx, tx, tenantID, item); err != nil {
		return run.Run{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_operations (tenant_id,endpoint,idempotency_key,request_hash,resource_id,created_at) VALUES ($1,$2,$3,$4,$5,$6)`, tenantID, "POST /v1/limen/runs", mutation.Key, mutation.Hash, item.ID, item.CreatedAt); err != nil {
		return run.Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return run.Run{}, err
	}
	return item, nil
}

// CompleteRunWithMutation 幂等地请求结束 PostgreSQL Run。
func (store *PostgresStore) CompleteRunWithMutation(ctx context.Context, tenantID, runID string, mutation run.Mutation) (run.Run, error) {
	return store.mutateRunWithMutation(ctx, tenantID, runID, mutation, "complete", "POST /v1/limen/runs/"+runID+"/complete")
}

// CancelRunWithMutation 幂等地请求取消 PostgreSQL Run。
func (store *PostgresStore) CancelRunWithMutation(ctx context.Context, tenantID, runID string, mutation run.Mutation) (run.Run, error) {
	return store.mutateRunWithMutation(ctx, tenantID, runID, mutation, "cancel", "POST /v1/limen/runs/"+runID+"/cancel")
}

func (store *PostgresStore) mutateRunWithMutation(ctx context.Context, tenantID, runID string, mutation run.Mutation, operation, endpoint string) (run.Run, error) {
	if mutation.Key == "" || mutation.Hash == "" {
		return run.Run{}, run.ErrIdempotencyKeyRequired
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return run.Run{}, err
	}
	defer tx.Rollback()
	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return run.Run{}, err
	}
	resourceID, existingHash, err := findOperationTx(ctx, tx, tenantID, endpoint, mutation.Key)
	if err == nil {
		if existingHash != mutation.Hash {
			return run.Run{}, run.ErrIdempotencyConflict
		}
		return getRunTx(ctx, tx, tenantID, resourceID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return run.Run{}, err
	}
	var currentState run.RunState
	var inFlight int
	if err := tx.QueryRowContext(ctx, `SELECT state,in_flight FROM runs WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, runID).Scan(&currentState, &inFlight); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return run.Run{}, run.ErrResourceNotFound
		}
		return run.Run{}, err
	}
	state := run.StateCancelled
	completeRequested := false
	if operation == "complete" {
		if currentState != run.StateActive {
			return run.Run{}, run.ErrInvalidRunTransition
		}
		state = run.StateCompleting
		completeRequested = true
		if inFlight == 0 {
			state = run.StateCompleted
		}
	}
	if operation == "cancel" && (currentState == run.StateCompleted || currentState == run.StateCancelled || currentState == run.StateDeadlineExceeded || currentState == run.StateSoftBudgetExhausted) {
		return run.Run{}, run.ErrInvalidRunTransition
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET state=$3, complete_requested=$4, updated_at=$5 WHERE tenant_id=$1 AND id=$2`, tenantID, runID, state, completeRequested, now); err != nil {
		return run.Run{}, err
	}
	if operation == "cancel" {
		var eventID int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO cancellation_events (tenant_id,run_id,created_at) VALUES ($1,$2,$3) RETURNING id`, tenantID, runID, now).Scan(&eventID); err != nil {
			return run.Run{}, err
		}
		// 通知只降低传播延迟，失败时仍依赖事件表轮询完成取消。
		_ = notifyCancellationEvent(ctx, tx, run.CancellationEvent{ID: eventID, TenantID: tenantID, RunID: runID, CreatedAt: now})
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_operations (tenant_id,endpoint,idempotency_key,request_hash,resource_id,created_at) VALUES ($1,$2,$3,$4,$5,$6)`, tenantID, endpoint, mutation.Key, mutation.Hash, runID, now); err != nil {
		return run.Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return run.Run{}, err
	}
	return store.GetRun(ctx, tenantID, runID)
}

// PollCancellationEvents 返回指定租户在游标之后的 PostgreSQL 取消事件。
func (store *PostgresStore) PollCancellationEvents(ctx context.Context, tenantID string, afterID int64, limit int) ([]run.CancellationEvent, error) {
	if store.db == nil {
		return nil, errors.New("postgres database is required")
	}
	if limit <= 0 {
		limit = 100
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,tenant_id,run_id,created_at FROM cancellation_events WHERE tenant_id=$1 AND id>$2 ORDER BY id LIMIT $3`, tenantID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []run.CancellationEvent
	for rows.Next() {
		var event run.CancellationEvent
		if err := rows.Scan(&event.ID, &event.TenantID, &event.RunID, &event.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return events, nil
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
	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return run.Request{}, err
	}
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
	leaseOwner := any(nil)
	leaseExpiresAt := any(nil)
	if input.LeaseOwner != "" {
		ttl := input.LeaseTTL
		if ttl <= 0 {
			ttl = run.RequestLeaseDuration
		}
		leaseOwner = input.LeaseOwner
		leaseExpiresAt = input.Now.Add(ttl)
		request.LeaseOwner = input.LeaseOwner
		request.LeaseExpiresAt = input.Now.Add(ttl)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO run_requests (tenant_id,id,run_id,endpoint,idempotency_key,request_hash,state,settlement_status,lease_owner,lease_expires_at,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, tenantID, request.ID, runID, request.Endpoint, request.IdempotencyKey, request.RequestHash, request.State, "pending", leaseOwner, leaseExpiresAt, request.CreatedAt, request.UpdatedAt)
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

// AcquireRequestLease 为 PostgreSQL 请求分配一个可续租的执行实例租约。
func (store *PostgresStore) AcquireRequestLease(ctx context.Context, tenantID, requestID, owner string, now time.Time, ttl time.Duration) (run.Request, error) {
	if store.db == nil {
		return run.Request{}, errors.New("postgres database is required")
	}
	if owner == "" {
		return run.Request{}, run.ErrLeaseUnavailable
	}
	if ttl <= 0 {
		ttl = run.RequestLeaseDuration
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return run.Request{}, err
	}
	defer tx.Rollback()
	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return run.Request{}, err
	}
	expiresAt := now.Add(ttl)
	var id string
	err = tx.QueryRowContext(ctx, `UPDATE run_requests SET lease_owner=$3, lease_expires_at=$4, updated_at=$5 WHERE tenant_id=$1 AND id=$2 AND state NOT IN ('settled','failed','cancelled','abandoned') AND (lease_owner IS NULL OR lease_expires_at <= $5 OR lease_owner=$3) RETURNING id`, tenantID, requestID, owner, expiresAt, now).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return store.leaseConflict(ctx, tx, tenantID, requestID)
	}
	if err != nil {
		return run.Request{}, err
	}
	request, err := getRequestTx(ctx, tx, tenantID, id)
	if err != nil {
		return run.Request{}, err
	}
	if err := tx.Commit(); err != nil {
		return run.Request{}, err
	}
	return request, nil
}

// RenewRequestLease 延长 PostgreSQL 请求的执行实例租约。
func (store *PostgresStore) RenewRequestLease(ctx context.Context, tenantID, requestID, owner string, now time.Time, ttl time.Duration) (run.Request, error) {
	if store.db == nil {
		return run.Request{}, errors.New("postgres database is required")
	}
	if ttl <= 0 {
		ttl = run.RequestLeaseDuration
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return run.Request{}, err
	}
	defer tx.Rollback()
	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return run.Request{}, err
	}
	var id string
	err = tx.QueryRowContext(ctx, `UPDATE run_requests SET lease_expires_at=$4, updated_at=$5 WHERE tenant_id=$1 AND id=$2 AND lease_owner=$3 AND lease_expires_at > $5 RETURNING id`, tenantID, requestID, owner, now.Add(ttl), now).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return store.leaseLost(ctx, tx, tenantID, requestID)
	}
	if err != nil {
		return run.Request{}, err
	}
	request, err := getRequestTx(ctx, tx, tenantID, id)
	if err != nil {
		return run.Request{}, err
	}
	if err := tx.Commit(); err != nil {
		return run.Request{}, err
	}
	return request, nil
}

// ReleaseRequestLease 释放 PostgreSQL 请求的执行实例租约。
func (store *PostgresStore) ReleaseRequestLease(ctx context.Context, tenantID, requestID, owner string, now time.Time) error {
	if store.db == nil {
		return errors.New("postgres database is required")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE run_requests SET lease_owner=NULL, lease_expires_at=NULL, updated_at=$4 WHERE tenant_id=$1 AND id=$2 AND lease_owner=$3`, tenantID, requestID, owner, now)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return run.ErrLeaseLost
	}
	return tx.Commit()
}

// RecoverExpiredRequests 将过期在途请求标记为未知费用并暂停 Run。
func (store *PostgresStore) RecoverExpiredRequests(ctx context.Context, tenantID string, now time.Time, limit int) ([]run.Request, error) {
	if store.db == nil {
		return nil, errors.New("postgres database is required")
	}
	if limit <= 0 {
		limit = 100
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,run_id FROM run_requests WHERE tenant_id=$1 AND lease_expires_at IS NOT NULL AND lease_expires_at <= $2 AND state NOT IN ('settled','failed','cancelled','abandoned') ORDER BY lease_expires_at LIMIT $3 FOR UPDATE SKIP LOCKED`, tenantID, now, limit)
	if err != nil {
		return nil, err
	}
	var requestIDs []struct {
		id    string
		runID string
	}
	for rows.Next() {
		var item struct {
			id    string
			runID string
		}
		if err := rows.Scan(&item.id, &item.runID); err != nil {
			rows.Close()
			return nil, err
		}
		requestIDs = append(requestIDs, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var recovered []run.Request
	for _, item := range requestIDs {
		requestID, runID := item.id, item.runID
		if _, err := tx.ExecContext(ctx, `UPDATE run_requests SET state='abandoned', settlement_status='pending', lease_owner=NULL, lease_expires_at=NULL, updated_at=$3 WHERE tenant_id=$1 AND id=$2`, tenantID, requestID, now); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET state=CASE WHEN state IN ('completed','cancelled','deadline_exceeded','soft_budget_exhausted') THEN state ELSE 'suspended_accounting' END, in_flight=GREATEST(in_flight-1,0), updated_at=$3 WHERE tenant_id=$1 AND id=$2`, tenantID, runID, now); err != nil {
			return nil, err
		}
		item, err := getRequestTx(ctx, tx, tenantID, requestID)
		if err != nil {
			return nil, err
		}
		recovered = append(recovered, item)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return recovered, nil
}

// leaseConflict 将租约更新失败区分为资源不存在或被其他实例持有。
func (store *PostgresStore) leaseConflict(ctx context.Context, tx *sql.Tx, tenantID, requestID string) (run.Request, error) {
	var state run.RequestState
	if err := tx.QueryRowContext(ctx, `SELECT state FROM run_requests WHERE tenant_id=$1 AND id=$2`, tenantID, requestID).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return run.Request{}, run.ErrResourceNotFound
		}
		return run.Request{}, err
	}
	if !isRequestInProgress(state) {
		return run.Request{}, run.ErrRequestAlreadyProcessed
	}
	return run.Request{}, run.ErrLeaseUnavailable
}

// leaseLost 将续租失败区分为资源不存在或租约已被回收。
func (store *PostgresStore) leaseLost(ctx context.Context, tx *sql.Tx, tenantID, requestID string) (run.Request, error) {
	var state run.RequestState
	if err := tx.QueryRowContext(ctx, `SELECT state FROM run_requests WHERE tenant_id=$1 AND id=$2`, tenantID, requestID).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return run.Request{}, run.ErrResourceNotFound
		}
		return run.Request{}, err
	}
	return run.Request{}, run.ErrLeaseLost
}

// RecordAttemptStarted 在访问 Provider 前记录一次本地 Attempt。
func (store *PostgresStore) RecordAttemptStarted(ctx context.Context, tenantID string, attempt run.Attempt) error {
	if store.db == nil {
		return errors.New("postgres database is required")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO attempts (tenant_id,id,request_id,target_id,provider,upstream_model,state,started_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, tenantID, attempt.ID, attempt.RequestID, attempt.TargetID, attempt.Provider, attempt.UpstreamModel, run.AttemptStarted, attempt.StartedAt); err != nil {
		return err
	}
	return tx.Commit()
}

// FinishAttempt 更新 PostgreSQL 中 Attempt 的终态和完成时间。
func (store *PostgresStore) FinishAttempt(ctx context.Context, tenantID, attemptID string, state run.AttemptState, finishedAt time.Time) error {
	if store.db == nil {
		return errors.New("postgres database is required")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE attempts SET state=$3, finished_at=$4 WHERE tenant_id=$1 AND id=$2`, tenantID, attemptID, state, finishedAt)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return run.ErrResourceNotFound
	}
	return tx.Commit()
}

// BeginSettlement 将执行结束的 Request 标记为待结算。
func (store *PostgresStore) BeginSettlement(ctx context.Context, tenantID, requestID string, now time.Time) (run.Request, error) {
	if store.db == nil {
		return run.Request{}, errors.New("postgres database is required")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return run.Request{}, err
	}
	defer tx.Rollback()
	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return run.Request{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE run_requests SET state=$3, settlement_status=$4, updated_at=$5 WHERE tenant_id=$1 AND id=$2 AND state NOT IN ('settled','failed','cancelled','abandoned')`, tenantID, requestID, run.RequestSettlementPending, "pending", now)
	if err != nil {
		return run.Request{}, err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return run.Request{}, run.ErrRequestNotSettleable
	}
	request, err := getRequestTx(ctx, tx, tenantID, requestID)
	if err != nil {
		return run.Request{}, err
	}
	if err := tx.Commit(); err != nil {
		return run.Request{}, err
	}
	return request, nil
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
	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return run.Request{}, err
	}
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
			_, err = tx.ExecContext(ctx, `UPDATE runs SET state=CASE WHEN state IN ('completed','cancelled','deadline_exceeded','soft_budget_exhausted') THEN state ELSE 'suspended_accounting' END, in_flight=GREATEST(in_flight-1,0), updated_at=$3 WHERE tenant_id=$1 AND id=$2`, tenantID, request.RunID, now)
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
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return run.Run{}, err
	}
	defer tx.Rollback()
	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return run.Run{}, err
	}
	item, err := getRunTx(ctx, tx, tenantID, runID)
	if err != nil {
		if errors.Is(err, run.ErrResourceNotFound) {
			return run.Run{}, ErrNotFound
		}
		return run.Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return run.Run{}, err
	}
	return item, nil
}

// GetRequest 读取不包含正文的 Request 状态。
func (store *PostgresStore) GetRequest(ctx context.Context, tenantID, requestID string) (run.Request, error) {
	if store.db == nil {
		return run.Request{}, errors.New("postgres database is required")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return run.Request{}, err
	}
	defer tx.Rollback()
	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return run.Request{}, err
	}
	item, err := getRequestTx(ctx, tx, tenantID, requestID)
	if err != nil {
		if errors.Is(err, run.ErrResourceNotFound) {
			return run.Request{}, ErrNotFound
		}
		return run.Request{}, err
	}
	if err := tx.Commit(); err != nil {
		return run.Request{}, err
	}
	return item, nil
}

// insertRunTx 在已有租户事务中插入 Run 行。
func insertRunTx(ctx context.Context, tx *sql.Tx, tenantID string, item run.Run) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO runs (tenant_id, id, state, soft_budget_nano_usd, settled_cost_nano_usd, deadline, max_parallelism, in_flight, strategy, config_version, complete_requested, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, tenantID, item.ID, item.State, item.SoftBudgetNanoUSD, item.SettledCostNanoUSD, item.Deadline, item.MaxParallelism, item.InFlight, item.Strategy, item.ConfigVersion, item.CompleteRequested, item.CreatedAt, item.UpdatedAt)
	return err
}

// setTenantTx 将租户上下文限制在当前数据库事务内。
func setTenantTx(ctx context.Context, tx *sql.Tx, tenantID string) error {
	_, err := tx.ExecContext(ctx, `SELECT set_config('limen.tenant_id',$1,true)`, tenantID)
	return err
}

// findOperationTx 查找控制面幂等操作的原始资源和请求哈希。
func findOperationTx(ctx context.Context, tx *sql.Tx, tenantID, endpoint, key string) (string, string, error) {
	var resourceID, requestHash string
	err := tx.QueryRowContext(ctx, `SELECT resource_id,request_hash FROM control_operations WHERE tenant_id=$1 AND endpoint=$2 AND idempotency_key=$3`, tenantID, endpoint, key).Scan(&resourceID, &requestHash)
	return resourceID, requestHash, err
}

// getRunTx 在已有事务内读取 Run 快照。
func getRunTx(ctx context.Context, tx *sql.Tx, tenantID, runID string) (run.Run, error) {
	var item run.Run
	err := tx.QueryRowContext(ctx, `SELECT id,tenant_id,state,soft_budget_nano_usd,settled_cost_nano_usd,deadline,max_parallelism,in_flight,strategy,config_version,complete_requested,created_at,updated_at FROM runs WHERE tenant_id=$1 AND id=$2`, tenantID, runID).Scan(&item.ID, &item.TenantID, &item.State, &item.SoftBudgetNanoUSD, &item.SettledCostNanoUSD, &item.Deadline, &item.MaxParallelism, &item.InFlight, &item.Strategy, &item.ConfigVersion, &item.CompleteRequested, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return run.Run{}, run.ErrResourceNotFound
	}
	return item, err
}

// getRequestTx 在已有事务内读取不含正文的 Request。
func getRequestTx(ctx context.Context, tx *sql.Tx, tenantID, requestID string) (run.Request, error) {
	var item run.Request
	var leaseOwner sql.NullString
	var leaseExpiresAt sql.NullTime
	err := tx.QueryRowContext(ctx, `SELECT id,tenant_id,run_id,endpoint,idempotency_key,request_hash,state,settlement_status,decision_id,ledger_recorded,lease_owner,lease_expires_at,created_at,updated_at FROM run_requests WHERE tenant_id=$1 AND id=$2`, tenantID, requestID).Scan(&item.ID, &item.TenantID, &item.RunID, &item.Endpoint, &item.IdempotencyKey, &item.RequestHash, &item.State, &item.SettlementStatus, &item.DecisionID, &item.LedgerRecorded, &leaseOwner, &leaseExpiresAt, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return run.Request{}, run.ErrResourceNotFound
	}
	if leaseOwner.Valid {
		item.LeaseOwner = leaseOwner.String
	}
	if leaseExpiresAt.Valid {
		item.LeaseExpiresAt = leaseExpiresAt.Time
	}
	return item, err
}

// isRequestInProgress 判断 PostgreSQL Request 是否仍占用幂等执行窗口。
func isRequestInProgress(state run.RequestState) bool {
	switch state {
	case run.RequestAdmitted, run.RequestDecisionReady, run.RequestExecuting, run.RequestSettlementPending:
		return true
	default:
		return false
	}
}

var _ Store = (*PostgresStore)(nil)
var _ run.Service = (*PostgresStore)(nil)
var _ run.ControlService = (*PostgresStore)(nil)
var _ run.LeaseService = (*PostgresStore)(nil)
var _ run.CancellationService = (*PostgresStore)(nil)
