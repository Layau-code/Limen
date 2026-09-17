package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/huz/limen/internal/run"
)

// QueueSettlement 将未完成结算写入持久化任务表，重复入队保持幂等。
func (store *PostgresStore) QueueSettlement(ctx context.Context, tenantID, requestID string, costNanoUSD *int64, nextAttemptAt time.Time) error {
	if store == nil || store.db == nil {
		return ErrDatabaseRequired
	}
	if nextAttemptAt.IsZero() {
		nextAttemptAt = time.Now().UTC()
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO settlement_jobs (tenant_id,request_id,cost_nano_usd,next_attempt_at,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$5) ON CONFLICT (tenant_id,request_id) DO UPDATE SET cost_nano_usd=COALESCE(EXCLUDED.cost_nano_usd,settlement_jobs.cost_nano_usd), next_attempt_at=LEAST(settlement_jobs.next_attempt_at,EXCLUDED.next_attempt_at), updated_at=EXCLUDED.updated_at`, tenantID, requestID, costNanoUSD, nextAttemptAt, time.Now().UTC())
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ClaimSettlementJobs 原子领取到期任务，避免多实例重复处理同一结算。
func (store *PostgresStore) ClaimSettlementJobs(ctx context.Context, tenantID, owner string, now time.Time, leaseTTL time.Duration, limit int) ([]run.SettlementJob, error) {
	if store == nil || store.db == nil {
		return nil, ErrDatabaseRequired
	}
	if owner == "" {
		return nil, run.ErrLeaseUnavailable
	}
	if leaseTTL <= 0 {
		leaseTTL = run.RequestLeaseDuration
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
	rows, err := tx.QueryContext(ctx, `SELECT request_id,cost_nano_usd,attempts,next_attempt_at FROM settlement_jobs WHERE tenant_id=$1 AND next_attempt_at <= $2 AND (lease_owner IS NULL OR lease_expires_at <= $2) ORDER BY next_attempt_at,request_id LIMIT $3 FOR UPDATE SKIP LOCKED`, tenantID, now, limit)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		requestID     string
		cost          sql.NullInt64
		attempts      int
		nextAttemptAt time.Time
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.requestID, &item.cost, &item.attempts, &item.nextAttemptAt); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	jobs := make([]run.SettlementJob, 0, len(candidates))
	leaseExpiresAt := now.Add(leaseTTL)
	for _, item := range candidates {
		if _, err := tx.ExecContext(ctx, `UPDATE settlement_jobs SET attempts=attempts+1,lease_owner=$3,lease_expires_at=$4,updated_at=$5 WHERE tenant_id=$1 AND request_id=$2`, tenantID, item.requestID, owner, leaseExpiresAt, now); err != nil {
			return nil, err
		}
		job := run.SettlementJob{TenantID: tenantID, RequestID: item.requestID, Attempts: item.attempts + 1, NextAttemptAt: item.nextAttemptAt, LeaseOwner: owner, LeaseExpiresAt: leaseExpiresAt}
		if item.cost.Valid {
			value := item.cost.Int64
			job.CostNanoUSD = &value
		}
		jobs = append(jobs, job)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return jobs, nil
}

// CompleteSettlementJob 删除当前实例已经处理完成的结算任务。
func (store *PostgresStore) CompleteSettlementJob(ctx context.Context, tenantID, requestID, owner string) error {
	if store == nil || store.db == nil {
		return ErrDatabaseRequired
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM settlement_jobs WHERE tenant_id=$1 AND request_id=$2 AND lease_owner=$3`, tenantID, requestID, owner)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return run.ErrLeaseLost
	}
	return tx.Commit()
}

// FailSettlementJob 释放当前租约并记录稳定失败码，等待下一次领取。
func (store *PostgresStore) FailSettlementJob(ctx context.Context, tenantID, requestID, owner string, nextAttemptAt time.Time, reason string) error {
	if store == nil || store.db == nil {
		return ErrDatabaseRequired
	}
	if reason != "settlement_retry_failed" {
		reason = "settlement_retry_failed"
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE settlement_jobs SET lease_owner=NULL,lease_expires_at=NULL,next_attempt_at=$4,last_error_code=$5,updated_at=$6 WHERE tenant_id=$1 AND request_id=$2 AND lease_owner=$3`, tenantID, requestID, owner, nextAttemptAt, reason, time.Now().UTC())
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return run.ErrLeaseLost
	}
	return tx.Commit()
}

var _ run.SettlementRecoveryService = (*PostgresStore)(nil)
