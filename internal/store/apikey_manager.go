package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/huz/limen/internal/auth"
)

const apiKeyMutationEndpoint = "POST /v1/limen/keys"

// PostgresAPIKeyManager 管理 PostgreSQL 中租户 API Key 的生命周期。
type PostgresAPIKeyManager struct {
	db     *sql.DB
	secret []byte
}

// NewPostgresAPIKeyManager 创建使用 HMAC 密钥生成和校验 API Key 的管理器。
func NewPostgresAPIKeyManager(db *sql.DB, hmacSecret string) *PostgresAPIKeyManager {
	return &PostgresAPIKeyManager{db: db, secret: []byte(hmacSecret)}
}

// Create 创建 API Key；明文只在首次成功响应中返回，数据库只保存摘要。
func (manager *PostgresAPIKeyManager) Create(ctx context.Context, tenantID string, scopes []auth.Scope, expiresAt *time.Time, mutation auth.APIKeyMutation) (auth.APIKeyRecord, string, error) {
	if err := validateAPIKeyMutation(tenantID, scopes, mutation); err != nil {
		return auth.APIKeyRecord{}, "", err
	}
	if manager == nil || manager.db == nil || len(manager.secret) == 0 {
		return auth.APIKeyRecord{}, "", ErrDatabaseRequired
	}
	tx, err := beginAPIKeyTx(ctx, manager.db, tenantID)
	if err != nil {
		return auth.APIKeyRecord{}, "", err
	}
	defer tx.Rollback()
	if prefix, requestHash, queryErr := findAPIKeyOperation(ctx, tx, tenantID, apiKeyMutationEndpoint, mutation.Key); queryErr == nil {
		if requestHash != mutation.Hash {
			return auth.APIKeyRecord{}, "", auth.ErrAPIKeyConflict
		}
		record, err := getAPIKeyTx(ctx, tx, tenantID, prefix)
		if err != nil {
			return auth.APIKeyRecord{}, "", err
		}
		if err := tx.Commit(); err != nil {
			return auth.APIKeyRecord{}, "", err
		}
		return record, "", nil
	} else if !errors.Is(queryErr, sql.ErrNoRows) {
		return auth.APIKeyRecord{}, "", queryErr
	}

	token, prefix, err := generateAPIKey()
	if err != nil {
		return auth.APIKeyRecord{}, "", err
	}
	encodedScopes, err := json.Marshal(scopes)
	if err != nil {
		return auth.APIKeyRecord{}, "", err
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO api_keys (public_prefix,tenant_id,digest,scopes,active,expires_at,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`, prefix, tenantID, hmacDigest(manager.secret, token), encodedScopes, true, expiresAt, now); err != nil {
		return auth.APIKeyRecord{}, "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO api_key_operations (tenant_id,endpoint,idempotency_key,request_hash,public_prefix,created_at) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (tenant_id,endpoint,idempotency_key) DO NOTHING`, tenantID, apiKeyMutationEndpoint, mutation.Key, mutation.Hash, prefix, now); err != nil {
		return auth.APIKeyRecord{}, "", err
	}
	storedPrefix, storedHash, err := findAPIKeyOperation(ctx, tx, tenantID, apiKeyMutationEndpoint, mutation.Key)
	if err != nil {
		return auth.APIKeyRecord{}, "", err
	}
	if storedHash != mutation.Hash {
		return auth.APIKeyRecord{}, "", auth.ErrAPIKeyConflict
	}
	if storedPrefix != prefix {
		record, err := getAPIKeyTx(ctx, tx, tenantID, storedPrefix)
		if err != nil {
			return auth.APIKeyRecord{}, "", err
		}
		if err := tx.Commit(); err != nil {
			return auth.APIKeyRecord{}, "", err
		}
		return record, "", nil
	}
	record := auth.APIKeyRecord{PublicPrefix: prefix, TenantID: tenantID, Scopes: append([]auth.Scope(nil), scopes...), Active: true, ExpiresAt: cloneTime(expiresAt), CreatedAt: now}
	if err := tx.Commit(); err != nil {
		return auth.APIKeyRecord{}, "", err
	}
	return record, token, nil
}

// List 返回租户的 API Key 元数据，不返回摘要或明文 Key。
func (manager *PostgresAPIKeyManager) List(ctx context.Context, tenantID string) ([]auth.APIKeyRecord, error) {
	if manager == nil || manager.db == nil {
		return nil, ErrDatabaseRequired
	}
	tx, err := beginAPIKeyTx(ctx, manager.db, tenantID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT public_prefix,scopes,active,expires_at,created_at FROM api_keys WHERE tenant_id=$1 ORDER BY created_at DESC,public_prefix DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]auth.APIKeyRecord, 0)
	for rows.Next() {
		var record auth.APIKeyRecord
		var scopesJSON []byte
		var expiresAt sql.NullTime
		if err := rows.Scan(&record.PublicPrefix, &scopesJSON, &record.Active, &expiresAt, &record.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(scopesJSON, &record.Scopes); err != nil || auth.ValidateAPIKeyScopes(record.Scopes) != nil {
			return nil, errors.New("stored api key scopes are invalid")
		}
		if expiresAt.Valid {
			expires := expiresAt.Time.UTC()
			record.ExpiresAt = &expires
		}
		record.TenantID = tenantID
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

// Rotate 原子创建新 Key 并停用旧 Key；明文只在首次成功响应中返回。
func (manager *PostgresAPIKeyManager) Rotate(ctx context.Context, tenantID, prefix string, scopes []auth.Scope, expiresAt *time.Time, mutation auth.APIKeyMutation) (auth.APIKeyRecord, auth.APIKeyRecord, string, error) {
	if strings.TrimSpace(tenantID) == "" || !auth.ValidatePublicPrefix(prefix) {
		return auth.APIKeyRecord{}, auth.APIKeyRecord{}, "", auth.ErrInvalidAPIKeyOperation
	}
	if err := validateAPIKeyMutation(tenantID, scopes, mutation); err != nil {
		return auth.APIKeyRecord{}, auth.APIKeyRecord{}, "", err
	}
	if manager == nil || manager.db == nil || len(manager.secret) == 0 {
		return auth.APIKeyRecord{}, auth.APIKeyRecord{}, "", ErrDatabaseRequired
	}
	tx, err := beginAPIKeyTx(ctx, manager.db, tenantID)
	if err != nil {
		return auth.APIKeyRecord{}, auth.APIKeyRecord{}, "", err
	}
	defer tx.Rollback()
	endpoint := apiKeyRotationEndpoint(prefix)
	if oldRecord, newRecord, err := readRotationOperation(ctx, tx, tenantID, prefix, endpoint, mutation); err != nil {
		return auth.APIKeyRecord{}, auth.APIKeyRecord{}, "", err
	} else if newRecord.PublicPrefix != "" {
		if err := tx.Commit(); err != nil {
			return auth.APIKeyRecord{}, auth.APIKeyRecord{}, "", err
		}
		return oldRecord, newRecord, "", nil
	}

	oldRecord, err := getAPIKeyTx(ctx, tx, tenantID, prefix)
	if errors.Is(err, auth.ErrAPIKeyNotFound) {
		return auth.APIKeyRecord{}, auth.APIKeyRecord{}, "", err
	}
	if err != nil {
		return auth.APIKeyRecord{}, auth.APIKeyRecord{}, "", err
	}
	var lockedActive bool
	if err := tx.QueryRowContext(ctx, `SELECT active FROM api_keys WHERE tenant_id=$1 AND public_prefix=$2 FOR UPDATE`, tenantID, prefix).Scan(&lockedActive); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return auth.APIKeyRecord{}, auth.APIKeyRecord{}, "", auth.ErrAPIKeyNotFound
		}
		return auth.APIKeyRecord{}, auth.APIKeyRecord{}, "", err
	}
	// 另一个相同幂等键的事务可能已在锁外完成，锁定后再次读取避免重复生成。
	if retryOld, retryNew, retryErr := readRotationOperation(ctx, tx, tenantID, prefix, endpoint, mutation); retryErr != nil {
		return auth.APIKeyRecord{}, auth.APIKeyRecord{}, "", retryErr
	} else if retryNew.PublicPrefix != "" {
		if err := tx.Commit(); err != nil {
			return auth.APIKeyRecord{}, auth.APIKeyRecord{}, "", err
		}
		return retryOld, retryNew, "", nil
	}
	if !lockedActive {
		return auth.APIKeyRecord{}, auth.APIKeyRecord{}, "", auth.ErrAPIKeyNotFound
	}

	token, newPrefix, err := generateAPIKey()
	if err != nil {
		return auth.APIKeyRecord{}, auth.APIKeyRecord{}, "", err
	}
	encodedScopes, err := json.Marshal(scopes)
	if err != nil {
		return auth.APIKeyRecord{}, auth.APIKeyRecord{}, "", err
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO api_keys (public_prefix,tenant_id,digest,scopes,active,expires_at,created_at) VALUES ($1,$2,$3,$4,TRUE,$5,$6)`, newPrefix, tenantID, hmacDigest(manager.secret, token), encodedScopes, expiresAt, now); err != nil {
		return auth.APIKeyRecord{}, auth.APIKeyRecord{}, "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE api_keys SET active=FALSE WHERE tenant_id=$1 AND public_prefix=$2`, tenantID, prefix); err != nil {
		return auth.APIKeyRecord{}, auth.APIKeyRecord{}, "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO api_key_operations (tenant_id,endpoint,idempotency_key,request_hash,public_prefix,created_at) VALUES ($1,$2,$3,$4,$5,$6)`, tenantID, endpoint, mutation.Key, mutation.Hash, newPrefix, now); err != nil {
		return auth.APIKeyRecord{}, auth.APIKeyRecord{}, "", err
	}
	newRecord := auth.APIKeyRecord{PublicPrefix: newPrefix, TenantID: tenantID, Scopes: append([]auth.Scope(nil), scopes...), Active: true, ExpiresAt: cloneTime(expiresAt), CreatedAt: now}
	if err := tx.Commit(); err != nil {
		return auth.APIKeyRecord{}, auth.APIKeyRecord{}, "", err
	}
	return oldRecord, newRecord, token, nil
}

// Revoke 停用一个租户 API Key，并以控制面幂等键保护重复请求。
func (manager *PostgresAPIKeyManager) Revoke(ctx context.Context, tenantID, prefix string, mutation auth.APIKeyMutation) error {
	if strings.TrimSpace(tenantID) == "" || !auth.ValidatePublicPrefix(prefix) || strings.TrimSpace(mutation.Key) == "" || strings.TrimSpace(mutation.Hash) == "" {
		return auth.ErrInvalidAPIKeyOperation
	}
	if manager == nil || manager.db == nil {
		return ErrDatabaseRequired
	}
	tx, err := beginAPIKeyTx(ctx, manager.db, tenantID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	endpoint := "POST /v1/limen/keys/" + prefix + "/revoke"
	if _, requestHash, queryErr := findAPIKeyOperation(ctx, tx, tenantID, endpoint, mutation.Key); queryErr == nil {
		if requestHash != mutation.Hash {
			return auth.ErrAPIKeyConflict
		}
		return tx.Commit()
	} else if !errors.Is(queryErr, sql.ErrNoRows) {
		return queryErr
	}
	var active bool
	err = tx.QueryRowContext(ctx, `SELECT active FROM api_keys WHERE tenant_id=$1 AND public_prefix=$2 FOR UPDATE`, tenantID, prefix).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.ErrAPIKeyNotFound
	}
	if err != nil {
		return err
	}
	if active {
		if _, err := tx.ExecContext(ctx, `UPDATE api_keys SET active=FALSE WHERE tenant_id=$1 AND public_prefix=$2`, tenantID, prefix); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO api_key_operations (tenant_id,endpoint,idempotency_key,request_hash,public_prefix,created_at) VALUES ($1,$2,$3,$4,$5,$6)`, tenantID, endpoint, mutation.Key, mutation.Hash, prefix, now); err != nil {
		return err
	}
	return tx.Commit()
}

// apiKeyRotationEndpoint 返回绑定旧 Key 前缀的稳定轮换操作名。
func apiKeyRotationEndpoint(prefix string) string {
	return "POST /v1/limen/keys/" + prefix + "/rotate"
}

// readRotationOperation 读取已有轮换结果，并校验幂等请求哈希。
func readRotationOperation(ctx context.Context, tx *sql.Tx, tenantID, oldPrefix, endpoint string, mutation auth.APIKeyMutation) (auth.APIKeyRecord, auth.APIKeyRecord, error) {
	newPrefix, requestHash, err := findAPIKeyOperation(ctx, tx, tenantID, endpoint, mutation.Key)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.APIKeyRecord{}, auth.APIKeyRecord{}, nil
	}
	if err != nil {
		return auth.APIKeyRecord{}, auth.APIKeyRecord{}, err
	}
	if requestHash != mutation.Hash {
		return auth.APIKeyRecord{}, auth.APIKeyRecord{}, auth.ErrAPIKeyConflict
	}
	oldRecord, err := getAPIKeyTx(ctx, tx, tenantID, oldPrefix)
	if err != nil {
		return auth.APIKeyRecord{}, auth.APIKeyRecord{}, err
	}
	newRecord, err := getAPIKeyTx(ctx, tx, tenantID, newPrefix)
	if err != nil {
		return auth.APIKeyRecord{}, auth.APIKeyRecord{}, err
	}
	return oldRecord, newRecord, nil
}

// validateAPIKeyMutation 校验创建操作的租户、Scope 和幂等摘要。
func validateAPIKeyMutation(tenantID string, scopes []auth.Scope, mutation auth.APIKeyMutation) error {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(mutation.Key) == "" || strings.TrimSpace(mutation.Hash) == "" {
		return auth.ErrInvalidAPIKeyOperation
	}
	return auth.ValidateAPIKeyScopes(scopes)
}

// generateAPIKey 生成公开前缀和只返回一次的高熵 Limen Key。
func generateAPIKey() (string, string, error) {
	var prefixBytes [8]byte
	var secretBytes [32]byte
	if _, err := rand.Read(prefixBytes[:]); err != nil {
		return "", "", err
	}
	if _, err := rand.Read(secretBytes[:]); err != nil {
		return "", "", err
	}
	prefix := hex.EncodeToString(prefixBytes[:])
	token := "lmn_live_" + prefix + "_" + hex.EncodeToString(secretBytes[:])
	return token, prefix, nil
}

// beginAPIKeyTx 开启设置了租户上下文的 API Key 事务。
func beginAPIKeyTx(ctx context.Context, db *sql.DB, tenantID string) (*sql.Tx, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, auth.ErrInvalidAPIKeyOperation
	}
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

// findAPIKeyOperation 读取同租户、同操作和幂等键的已有记录。
func findAPIKeyOperation(ctx context.Context, tx *sql.Tx, tenantID, endpoint, key string) (string, string, error) {
	var prefix, requestHash string
	err := tx.QueryRowContext(ctx, `SELECT public_prefix,request_hash FROM api_key_operations WHERE tenant_id=$1 AND endpoint=$2 AND idempotency_key=$3`, tenantID, endpoint, key).Scan(&prefix, &requestHash)
	return prefix, requestHash, err
}

// getAPIKeyTx 将数据库 Key 元数据解析为不包含摘要的领域记录。
func getAPIKeyTx(ctx context.Context, tx *sql.Tx, tenantID, prefix string) (auth.APIKeyRecord, error) {
	var record auth.APIKeyRecord
	var scopesJSON []byte
	var expiresAt sql.NullTime
	err := tx.QueryRowContext(ctx, `SELECT public_prefix,scopes,active,expires_at,created_at FROM api_keys WHERE tenant_id=$1 AND public_prefix=$2`, tenantID, prefix).Scan(&record.PublicPrefix, &scopesJSON, &record.Active, &expiresAt, &record.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.APIKeyRecord{}, auth.ErrAPIKeyNotFound
	}
	if err != nil {
		return auth.APIKeyRecord{}, err
	}
	if err := json.Unmarshal(scopesJSON, &record.Scopes); err != nil || auth.ValidateAPIKeyScopes(record.Scopes) != nil {
		return auth.APIKeyRecord{}, errors.New("stored api key scopes are invalid")
	}
	if expiresAt.Valid {
		expires := expiresAt.Time.UTC()
		record.ExpiresAt = &expires
	}
	record.TenantID = tenantID
	return record, nil
}

// cloneTime 复制可选时间，避免调用方修改存储记录。
func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := value.UTC()
	return &cloned
}

var _ auth.APIKeyManager = (*PostgresAPIKeyManager)(nil)
