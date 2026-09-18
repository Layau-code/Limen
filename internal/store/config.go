package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/huz/limen/internal/config"
	"github.com/huz/limen/internal/configstore"
)

// PostgresConfigStore 将租户配置版本保存到 PostgreSQL，并使用 RLS 约束查询边界。
type PostgresConfigStore struct {
	db *sql.DB
}

// NewPostgresConfigStore 创建 PostgreSQL 配置版本存储。
func NewPostgresConfigStore(db *sql.DB) *PostgresConfigStore {
	return &PostgresConfigStore{db: db}
}

// Create 校验并幂等保存一个不可变配置版本。
func (store *PostgresConfigStore) Create(ctx context.Context, tenantID string, document []byte) (configstore.Record, error) {
	if store == nil || store.db == nil {
		return configstore.Record{}, ErrDatabaseRequired
	}
	version, models, routing, err := parseConfig(document)
	if err != nil {
		return configstore.Record{}, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return configstore.Record{}, err
	}
	defer tx.Rollback()
	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return configstore.Record{}, err
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `INSERT INTO config_versions (tenant_id,version,state,document,created_at) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (tenant_id,version) DO NOTHING`, tenantID, version, configstore.StateDraft, document, now)
	if err != nil {
		return configstore.Record{}, err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		record, err := getConfigTx(ctx, tx, tenantID, version)
		if err != nil {
			return configstore.Record{}, err
		}
		if err := tx.Commit(); err != nil {
			return configstore.Record{}, err
		}
		return record, nil
	}
	record := configstore.Record{TenantID: tenantID, Version: version, State: configstore.StateDraft, Document: append([]byte(nil), document...), Models: models, Routing: routing, CreatedAt: now}
	if err := tx.Commit(); err != nil {
		return configstore.Record{}, err
	}
	return record, nil
}

// List 返回租户配置版本并按创建时间倒序排列。
func (store *PostgresConfigStore) List(ctx context.Context, tenantID string) ([]configstore.Record, error) {
	tx, err := store.beginTenantTx(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT version,state,document,created_at,published_at FROM config_versions WHERE tenant_id=$1 ORDER BY created_at DESC,version DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]configstore.Record, 0)
	for rows.Next() {
		record, err := scanConfigRow(rows, tenantID)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// Get 返回租户指定的配置版本。
func (store *PostgresConfigStore) Get(ctx context.Context, tenantID, version string) (configstore.Record, error) {
	tx, err := store.beginTenantTx(ctx, tenantID)
	if err != nil {
		return configstore.Record{}, err
	}
	defer tx.Rollback()
	record, err := getConfigTx(ctx, tx, tenantID, version)
	if err != nil {
		return configstore.Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return configstore.Record{}, err
	}
	return record, nil
}

// GetPublished 返回租户当前唯一已发布的配置版本。
func (store *PostgresConfigStore) GetPublished(ctx context.Context, tenantID string) (configstore.Record, error) {
	tx, err := store.beginTenantTx(ctx, tenantID)
	if err != nil {
		return configstore.Record{}, err
	}
	defer tx.Rollback()
	record, err := getConfigTx(ctx, tx, tenantID, "")
	if err == nil {
		if err := tx.Commit(); err != nil {
			return configstore.Record{}, err
		}
		return record, nil
	}
	return configstore.Record{}, err
}

// Publish 原子切换租户当前版本，并保留旧版本审计记录。
func (store *PostgresConfigStore) Publish(ctx context.Context, tenantID, version string) (configstore.Record, error) {
	return store.publish(ctx, tenantID, version, nil)
}

// PublishWithMutation 幂等发布配置版本并返回首次发布结果。
func (store *PostgresConfigStore) PublishWithMutation(ctx context.Context, tenantID, version string, mutation configstore.Mutation) (configstore.Record, error) {
	if strings.TrimSpace(mutation.Key) == "" || strings.TrimSpace(mutation.Hash) == "" {
		return configstore.Record{}, configstore.ErrIdempotencyConflict
	}
	return store.publish(ctx, tenantID, version, &mutation)
}

// publish 在同一事务中切换配置并保存跨实例幂等记录。
func (store *PostgresConfigStore) publish(ctx context.Context, tenantID, version string, mutation *configstore.Mutation) (configstore.Record, error) {
	tx, err := store.beginTenantTx(ctx, tenantID)
	if err != nil {
		return configstore.Record{}, err
	}
	defer tx.Rollback()
	record, err := getConfigTxForUpdate(ctx, tx, tenantID, version)
	if err != nil {
		return configstore.Record{}, err
	}
	if mutation != nil {
		var existingHash, existingVersion string
		err = tx.QueryRowContext(ctx, `SELECT request_hash,version FROM config_operations WHERE tenant_id=$1 AND endpoint=$2 AND idempotency_key=$3`, tenantID, "publish_config", mutation.Key).Scan(&existingHash, &existingVersion)
		if err == nil {
			if existingHash != mutation.Hash {
				return configstore.Record{}, configstore.ErrIdempotencyConflict
			}
			record, err = getConfigTx(ctx, tx, tenantID, existingVersion)
			if err != nil {
				return configstore.Record{}, err
			}
			if err := tx.Commit(); err != nil {
				return configstore.Record{}, err
			}
			return record, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return configstore.Record{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO config_operations (tenant_id,endpoint,idempotency_key,request_hash,version,created_at) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (tenant_id,endpoint,idempotency_key) DO NOTHING`, tenantID, "publish_config", mutation.Key, mutation.Hash, version, time.Now().UTC()); err != nil {
			return configstore.Record{}, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT request_hash,version FROM config_operations WHERE tenant_id=$1 AND endpoint=$2 AND idempotency_key=$3`, tenantID, "publish_config", mutation.Key).Scan(&existingHash, &existingVersion); err != nil {
			return configstore.Record{}, err
		}
		if existingHash != mutation.Hash {
			return configstore.Record{}, configstore.ErrIdempotencyConflict
		}
		if existingVersion != version {
			record, err = getConfigTx(ctx, tx, tenantID, existingVersion)
			if err != nil {
				return configstore.Record{}, err
			}
			if err := tx.Commit(); err != nil {
				return configstore.Record{}, err
			}
			return record, nil
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE config_versions SET state=$1 WHERE tenant_id=$2 AND state=$3`, configstore.StateSuperseded, tenantID, configstore.StatePublished); err != nil {
		return configstore.Record{}, err
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE config_versions SET state=$1,published_at=$2 WHERE tenant_id=$3 AND version=$4`, configstore.StatePublished, now, tenantID, version); err != nil {
		return configstore.Record{}, err
	}
	record.State = configstore.StatePublished
	record.PublishedAt = &now
	_ = notifyConfigChange(ctx, tx, ConfigChange{TenantID: tenantID, Version: record.Version})
	if err := tx.Commit(); err != nil {
		return configstore.Record{}, err
	}
	return record, nil
}

// beginTenantTx 开启设置了数据库租户上下文的事务。
func (store *PostgresConfigStore) beginTenantTx(ctx context.Context, tenantID string) (*sql.Tx, error) {
	if store == nil || store.db == nil {
		return nil, ErrDatabaseRequired
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

// getConfigTx 在已有事务中读取配置，version 为空时读取已发布版本。
func getConfigTx(ctx context.Context, tx *sql.Tx, tenantID, version string) (configstore.Record, error) {
	query := `SELECT version,state,document,created_at,published_at FROM config_versions WHERE tenant_id=$1 AND version=$2`
	args := []any{tenantID, version}
	if version == "" {
		query = `SELECT version,state,document,created_at,published_at FROM config_versions WHERE tenant_id=$1 AND state=$2`
		args = []any{tenantID, configstore.StatePublished}
	}
	return scanConfigRow(tx.QueryRowContext(ctx, query, args...), tenantID)
}

// getConfigTxForUpdate 读取并锁定待发布版本，避免并发发布覆盖状态。
func getConfigTxForUpdate(ctx context.Context, tx *sql.Tx, tenantID, version string) (configstore.Record, error) {
	return scanConfigRow(tx.QueryRowContext(ctx, `SELECT version,state,document,created_at,published_at FROM config_versions WHERE tenant_id=$1 AND version=$2 FOR UPDATE`, tenantID, version), tenantID)
}

type rowScanner interface {
	Scan(...any) error
}

// scanConfigRow 将数据库行解析为经过校验的配置记录。
func scanConfigRow(row rowScanner, tenantID string) (configstore.Record, error) {
	var record configstore.Record
	var document []byte
	var publishedAt sql.NullTime
	if err := row.Scan(&record.Version, &record.State, &document, &record.CreatedAt, &publishedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return configstore.Record{}, configstore.ErrNotFound
		}
		return configstore.Record{}, err
	}
	version, models, routing, err := parseConfig(document)
	if err != nil {
		return configstore.Record{}, err
	}
	if version != record.Version {
		return configstore.Record{}, errors.New("stored config version hash mismatch")
	}
	record.TenantID = tenantID
	record.Document = append([]byte(nil), document...)
	record.Models = models
	record.Routing = routing
	if publishedAt.Valid {
		record.PublishedAt = &publishedAt.Time
	}
	return record, nil
}

// parseConfig 复用统一配置解析器，避免文件和数据库产生两套规则。
func parseConfig(document []byte) (string, []config.Model, config.Routing, error) {
	models, routing, version, err := config.ParseModels(document, config.DefaultRouting())
	return version, models, routing, err
}

var _ configstore.Store = (*PostgresConfigStore)(nil)
var _ configstore.MutationStore = (*PostgresConfigStore)(nil)
