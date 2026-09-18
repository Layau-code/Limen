package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/huz/limen/internal/approval"
)

const (
	approvalCreateEndpoint = "create_config_approval"
	approvalApprovePrefix  = "approve_config_approval:"
	approvalRejectPrefix   = "reject_config_approval:"
)

// PostgresApprovalStore 将配置发布审批保存到 PostgreSQL，并使用 RLS 隔离租户。
type PostgresApprovalStore struct {
	db *sql.DB
}

// NewPostgresApprovalStore 创建 PostgreSQL 审批存储。
func NewPostgresApprovalStore(db *sql.DB) *PostgresApprovalStore {
	return &PostgresApprovalStore{db: db}
}

// Create 创建绑定到配置版本和发布幂等键的待审批记录。
func (store *PostgresApprovalStore) Create(ctx context.Context, tenantID, configVersion, publishKey, requestHash, requestedBy string, mutation approval.Mutation) (approval.Record, error) {
	if store == nil || store.db == nil {
		return approval.Record{}, ErrDatabaseRequired
	}
	if invalidApprovalCreate(tenantID, configVersion, publishKey, requestHash, requestedBy, mutation) {
		return approval.Record{}, approval.ErrInvalid
	}
	tx, err := store.beginTx(ctx, tenantID)
	if err != nil {
		return approval.Record{}, err
	}
	defer tx.Rollback()
	if existing, found, err := findApprovalOperation(ctx, tx, tenantID, approvalCreateEndpoint, mutation.Key); err != nil {
		return approval.Record{}, err
	} else if found {
		if existing.Hash != mutation.Hash {
			return approval.Record{}, approval.ErrIdempotencyConflict
		}
		record, err := getApprovalTx(ctx, tx, tenantID, configVersion, existing.ApprovalID, false)
		if err != nil {
			return approval.Record{}, err
		}
		if err := tx.Commit(); err != nil {
			return approval.Record{}, err
		}
		return record, nil
	}
	now := time.Now().UTC()
	id, err := approval.NewID()
	if err != nil {
		return approval.Record{}, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO config_approvals (tenant_id,approval_id,config_version,publish_idempotency_key,request_hash,requested_by,state,expires_at,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9) ON CONFLICT (tenant_id,config_version,publish_idempotency_key) DO NOTHING`, tenantID, id, configVersion, publishKey, requestHash, requestedBy, approval.StatePending, now.Add(approval.DefaultTTL), now)
	if err != nil {
		return approval.Record{}, err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		var existingID string
		if err := tx.QueryRowContext(ctx, `SELECT approval_id FROM config_approvals WHERE tenant_id=$1 AND config_version=$2 AND publish_idempotency_key=$3`, tenantID, configVersion, publishKey).Scan(&existingID); err != nil {
			return approval.Record{}, err
		}
		existing, found, err := findApprovalOperation(ctx, tx, tenantID, approvalCreateEndpoint, mutation.Key)
		if err != nil {
			return approval.Record{}, err
		}
		if !found {
			return approval.Record{}, approval.ErrBindingConflict
		}
		if existing.Hash != mutation.Hash || existing.ApprovalID != existingID {
			return approval.Record{}, approval.ErrIdempotencyConflict
		}
		record, err := getApprovalTx(ctx, tx, tenantID, configVersion, existingID, false)
		if err != nil {
			return approval.Record{}, err
		}
		if err := tx.Commit(); err != nil {
			return approval.Record{}, err
		}
		return record, nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO config_approval_operations (tenant_id,endpoint,idempotency_key,request_hash,approval_id,created_at) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, tenantID, approvalCreateEndpoint, mutation.Key, mutation.Hash, id, now); err != nil {
		return approval.Record{}, err
	}
	existing, found, err := findApprovalOperation(ctx, tx, tenantID, approvalCreateEndpoint, mutation.Key)
	if err != nil {
		return approval.Record{}, err
	}
	if !found || existing.Hash != mutation.Hash || existing.ApprovalID != id {
		return approval.Record{}, approval.ErrIdempotencyConflict
	}
	if err := tx.Commit(); err != nil {
		return approval.Record{}, err
	}
	return approval.Record{TenantID: tenantID, ID: id, ConfigVersion: configVersion, PublishIdempotencyKey: publishKey, RequestHash: requestHash, RequestedBy: requestedBy, State: approval.StatePending, ExpiresAt: now.Add(approval.DefaultTTL), CreatedAt: now, UpdatedAt: now}, nil
}

// Get 返回审批状态，并在读取时持久化过期状态。
func (store *PostgresApprovalStore) Get(ctx context.Context, tenantID, configVersion, approvalID string) (approval.Record, error) {
	tx, err := store.beginTx(ctx, tenantID)
	if err != nil {
		return approval.Record{}, err
	}
	defer tx.Rollback()
	record, err := getApprovalTx(ctx, tx, tenantID, configVersion, approvalID, true)
	if err != nil {
		return approval.Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return approval.Record{}, err
	}
	return record, nil
}

// Approve 由与申请者不同的身份批准配置发布。
func (store *PostgresApprovalStore) Approve(ctx context.Context, tenantID, configVersion, approvalID, actorID string, mutation approval.Mutation) (approval.Record, error) {
	return store.decide(ctx, tenantID, configVersion, approvalID, actorID, mutation, true)
}

// Reject 由与申请者不同的身份拒绝配置发布。
func (store *PostgresApprovalStore) Reject(ctx context.Context, tenantID, configVersion, approvalID, actorID string, mutation approval.Mutation) (approval.Record, error) {
	return store.decide(ctx, tenantID, configVersion, approvalID, actorID, mutation, false)
}

// ValidateForPublish 检查发布绑定是否指向可消费的审批。
func (store *PostgresApprovalStore) ValidateForPublish(ctx context.Context, binding approval.Binding) (approval.Record, error) {
	if err := validateApprovalBinding(binding); err != nil {
		return approval.Record{}, err
	}
	tx, err := store.beginTx(ctx, binding.TenantID)
	if err != nil {
		return approval.Record{}, err
	}
	defer tx.Rollback()
	record, err := getApprovalTx(ctx, tx, binding.TenantID, binding.ConfigVersion, binding.ApprovalID, true)
	if err != nil {
		return approval.Record{}, err
	}
	if record.State == approval.StateExpired {
		if err := tx.Commit(); err != nil {
			return approval.Record{}, err
		}
		return approval.Record{}, approval.ErrExpired
	}
	if err := checkApprovalBinding(record, binding); err != nil {
		return approval.Record{}, err
	}
	if record.State != approval.StateApproved && record.State != approval.StateConsumed {
		return approval.Record{}, approvalStateError(record.State)
	}
	if err := tx.Commit(); err != nil {
		return approval.Record{}, err
	}
	return record, nil
}

// Consume 将已批准的审批标记为已消费，供内存或非原子配置存储使用。
func (store *PostgresApprovalStore) Consume(ctx context.Context, binding approval.Binding) (approval.Record, error) {
	if err := validateApprovalBinding(binding); err != nil {
		return approval.Record{}, err
	}
	tx, err := store.beginTx(ctx, binding.TenantID)
	if err != nil {
		return approval.Record{}, err
	}
	defer tx.Rollback()
	record, err := getApprovalTx(ctx, tx, binding.TenantID, binding.ConfigVersion, binding.ApprovalID, true)
	if err != nil {
		return approval.Record{}, err
	}
	if record.State == approval.StateExpired {
		if err := tx.Commit(); err != nil {
			return approval.Record{}, err
		}
		return approval.Record{}, approval.ErrExpired
	}
	if err := checkApprovalBinding(record, binding); err != nil {
		return approval.Record{}, err
	}
	if record.State == approval.StateConsumed {
		if err := tx.Commit(); err != nil {
			return approval.Record{}, err
		}
		return record, nil
	}
	if record.State != approval.StateApproved {
		return approval.Record{}, approvalStateError(record.State)
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE config_approvals SET state=$1,updated_at=$2 WHERE tenant_id=$3 AND approval_id=$4`, approval.StateConsumed, now, binding.TenantID, binding.ApprovalID); err != nil {
		return approval.Record{}, err
	}
	record.State = approval.StateConsumed
	record.UpdatedAt = now
	if err := tx.Commit(); err != nil {
		return approval.Record{}, err
	}
	return record, nil
}

// decide 在事务中完成审批决定、身份分离和操作幂等。
func (store *PostgresApprovalStore) decide(ctx context.Context, tenantID, configVersion, approvalID, actorID string, mutation approval.Mutation, approve bool) (approval.Record, error) {
	if strings.TrimSpace(actorID) == "" || strings.TrimSpace(mutation.Key) == "" || strings.TrimSpace(mutation.Hash) == "" {
		return approval.Record{}, approval.ErrInvalid
	}
	tx, err := store.beginTx(ctx, tenantID)
	if err != nil {
		return approval.Record{}, err
	}
	defer tx.Rollback()
	endpoint := approvalRejectPrefix + approvalID
	if approve {
		endpoint = approvalApprovePrefix + approvalID
	}
	if existing, found, err := findApprovalOperation(ctx, tx, tenantID, endpoint, mutation.Key); err != nil {
		return approval.Record{}, err
	} else if found {
		if existing.Hash != mutation.Hash {
			return approval.Record{}, approval.ErrIdempotencyConflict
		}
		record, err := getApprovalTx(ctx, tx, tenantID, configVersion, existing.ApprovalID, false)
		if err != nil {
			return approval.Record{}, err
		}
		if err := tx.Commit(); err != nil {
			return approval.Record{}, err
		}
		return record, nil
	}
	record, err := getApprovalTx(ctx, tx, tenantID, configVersion, approvalID, true)
	if err != nil {
		return approval.Record{}, err
	}
	// 先等待审批行锁，再重查操作记录，避免并发同键请求看到旧状态而误报冲突。
	if existing, found, err := findApprovalOperation(ctx, tx, tenantID, endpoint, mutation.Key); err != nil {
		return approval.Record{}, err
	} else if found {
		if existing.Hash != mutation.Hash {
			return approval.Record{}, approval.ErrIdempotencyConflict
		}
		if err := tx.Commit(); err != nil {
			return approval.Record{}, err
		}
		return record, nil
	}
	if record.State == approval.StateExpired {
		if err := tx.Commit(); err != nil {
			return approval.Record{}, err
		}
		return approval.Record{}, approval.ErrExpired
	}
	if record.State != approval.StatePending {
		return approval.Record{}, approvalStateError(record.State)
	}
	if record.RequestedBy == actorID {
		return approval.Record{}, approval.ErrActorNotDistinct
	}
	now := time.Now().UTC()
	state := approval.StateRejected
	if approve {
		state = approval.StateApproved
		record.ApprovedBy = actorID
	}
	if _, err := tx.ExecContext(ctx, `UPDATE config_approvals SET state=$1,approved_by=$2,updated_at=$3 WHERE tenant_id=$4 AND approval_id=$5`, state, nullableString(record.ApprovedBy), now, tenantID, approvalID); err != nil {
		return approval.Record{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO config_approval_operations (tenant_id,endpoint,idempotency_key,request_hash,approval_id,created_at) VALUES ($1,$2,$3,$4,$5,$6)`, tenantID, endpoint, mutation.Key, mutation.Hash, approvalID, now); err != nil {
		return approval.Record{}, err
	}
	record.State = state
	record.UpdatedAt = now
	if err := tx.Commit(); err != nil {
		return approval.Record{}, err
	}
	return record, nil
}

// beginTx 开启带租户上下文的审批事务。
func (store *PostgresApprovalStore) beginTx(ctx context.Context, tenantID string) (*sql.Tx, error) {
	if store == nil || store.db == nil {
		return nil, ErrDatabaseRequired
	}
	return beginApprovalTx(ctx, store.db, tenantID)
}

type approvalOperation struct {
	Hash       string
	ApprovalID string
}

// findApprovalOperation 查询审批控制面操作的幂等记录。
func findApprovalOperation(ctx context.Context, tx *sql.Tx, tenantID, endpoint, key string) (approvalOperation, bool, error) {
	var operation approvalOperation
	err := tx.QueryRowContext(ctx, `SELECT request_hash,approval_id FROM config_approval_operations WHERE tenant_id=$1 AND endpoint=$2 AND idempotency_key=$3`, tenantID, endpoint, key).Scan(&operation.Hash, &operation.ApprovalID)
	if errors.Is(err, sql.ErrNoRows) {
		return approvalOperation{}, false, nil
	}
	return operation, err == nil, err
}

// getApprovalTx 在事务中读取审批记录，并可选加行锁防止并发决定。
func getApprovalTx(ctx context.Context, tx *sql.Tx, tenantID, configVersion, approvalID string, lock bool) (approval.Record, error) {
	query := `SELECT approval_id,config_version,publish_idempotency_key,request_hash,requested_by,approved_by,state,expires_at,created_at,updated_at FROM config_approvals WHERE tenant_id=$1 AND config_version=$2 AND approval_id=$3`
	if lock {
		query += ` FOR UPDATE`
	}
	var record approval.Record
	var approvedBy sql.NullString
	if err := tx.QueryRowContext(ctx, query, tenantID, configVersion, approvalID).Scan(&record.ID, &record.ConfigVersion, &record.PublishIdempotencyKey, &record.RequestHash, &record.RequestedBy, &approvedBy, &record.State, &record.ExpiresAt, &record.CreatedAt, &record.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return approval.Record{}, approval.ErrNotFound
		}
		return approval.Record{}, err
	}
	record.TenantID = tenantID
	if approvedBy.Valid {
		record.ApprovedBy = approvedBy.String
	}
	if record.State == approval.StatePending && !time.Now().UTC().Before(record.ExpiresAt) {
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx, `UPDATE config_approvals SET state=$1,updated_at=$2 WHERE tenant_id=$3 AND approval_id=$4`, approval.StateExpired, now, tenantID, approvalID); err != nil {
			return approval.Record{}, err
		}
		record.State = approval.StateExpired
		record.UpdatedAt = now
	}
	return record, nil
}

// invalidApprovalCreate 判断审批申请是否缺少必要字段。
func invalidApprovalCreate(tenantID, configVersion, publishKey, requestHash, requestedBy string, mutation approval.Mutation) bool {
	return strings.TrimSpace(tenantID) == "" || strings.TrimSpace(configVersion) == "" || strings.TrimSpace(publishKey) == "" || strings.TrimSpace(requestHash) == "" || strings.TrimSpace(requestedBy) == "" || strings.TrimSpace(mutation.Key) == "" || strings.TrimSpace(mutation.Hash) == ""
}

// validateApprovalBinding 校验发布消费审批的字段完整性。
func validateApprovalBinding(binding approval.Binding) error {
	if strings.TrimSpace(binding.TenantID) == "" || strings.TrimSpace(binding.ConfigVersion) == "" || strings.TrimSpace(binding.ApprovalID) == "" || strings.TrimSpace(binding.PublishIdempotencyKey) == "" || strings.TrimSpace(binding.RequestHash) == "" || strings.TrimSpace(binding.Publisher) == "" {
		return approval.ErrInvalid
	}
	return nil
}

// checkApprovalBinding 确认数据库审批记录与发布请求严格匹配。
func checkApprovalBinding(record approval.Record, binding approval.Binding) error {
	if record.TenantID != binding.TenantID || record.ConfigVersion != binding.ConfigVersion || record.ID != binding.ApprovalID || record.PublishIdempotencyKey != binding.PublishIdempotencyKey || record.RequestHash != binding.RequestHash {
		return approval.ErrBindingConflict
	}
	if record.RequestedBy == binding.Publisher {
		return approval.ErrActorNotDistinct
	}
	return nil
}

// approvalStateError 将审批状态冲突转换为稳定错误。
func approvalStateError(state string) error {
	if state == approval.StateExpired {
		return approval.ErrExpired
	}
	return approval.ErrStateConflict
}

// beginApprovalTx 开启事务并设置 PostgreSQL 租户上下文。
func beginApprovalTx(ctx context.Context, db *sql.DB, tenantID string) (*sql.Tx, error) {
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

var _ approval.Store = (*PostgresApprovalStore)(nil)
