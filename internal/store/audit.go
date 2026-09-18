package store

import (
	"context"
	"database/sql"
	"strings"

	"github.com/huz/limen/internal/audit"
)

// PostgresAuditStore 将管理操作摘要保存到带 RLS 的 PostgreSQL 表。
type PostgresAuditStore struct {
	db *sql.DB
}

// NewPostgresAuditStore 创建 PostgreSQL 审计存储。
func NewPostgresAuditStore(db *sql.DB) *PostgresAuditStore {
	return &PostgresAuditStore{db: db}
}

// Append 在租户事务中追加一个不含敏感正文的审计事件。
func (store *PostgresAuditStore) Append(ctx context.Context, event audit.Event) error {
	if store == nil || store.db == nil {
		return ErrDatabaseRequired
	}
	if err := event.Validate(); err != nil {
		return err
	}
	tx, err := beginAuditTx(ctx, store.db, event.TenantID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events (tenant_id,event_id,actor_id,action,resource_type,resource_id,outcome,request_hash,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (tenant_id,event_id) DO NOTHING`, event.TenantID, event.ID, auditActorID(event.ActorID), event.Action, event.ResourceType, event.ResourceID, event.Outcome, nullableString(event.RequestHash), event.CreatedAt.UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

// List 返回租户最近的管理操作摘要，不返回动态正文或密钥。
func (store *PostgresAuditStore) List(ctx context.Context, tenantID string, limit int) ([]audit.Event, error) {
	if store == nil || store.db == nil {
		return nil, ErrDatabaseRequired
	}
	if strings.TrimSpace(tenantID) == "" || limit <= 0 {
		return nil, audit.ErrInvalidEvent
	}
	tx, err := beginAuditTx(ctx, store.db, tenantID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT event_id,actor_id,action,resource_type,resource_id,outcome,request_hash,created_at FROM audit_events WHERE tenant_id=$1 ORDER BY created_at DESC,event_id DESC LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]audit.Event, 0)
	for rows.Next() {
		var event audit.Event
		var actorID sql.NullString
		var requestHash sql.NullString
		if err := rows.Scan(&event.ID, &actorID, &event.Action, &event.ResourceType, &event.ResourceID, &event.Outcome, &requestHash, &event.CreatedAt); err != nil {
			return nil, err
		}
		event.TenantID = tenantID
		if actorID.Valid {
			event.ActorID = actorID.String
		}
		if requestHash.Valid {
			event.RequestHash = requestHash.String
		}
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// nullableString 将可选哈希转换为 PostgreSQL NULL。
func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// auditActorID 为历史事件和内部调用提供稳定的非敏感执行者值。
func auditActorID(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

var _ audit.Store = (*PostgresAuditStore)(nil)

// beginAuditTx 开启设置了租户上下文的审计事务。
func beginAuditTx(ctx context.Context, db *sql.DB, tenantID string) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}
