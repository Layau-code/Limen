package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/huz/limen/internal/credentialstore"
)

// PostgresCredentialStore 将加密 Provider 凭据保存到 PostgreSQL。
type PostgresCredentialStore struct {
	db    *sql.DB
	vault *credentialstore.Vault
}

// NewPostgresCredentialStore 创建绑定主密钥的 PostgreSQL 凭据存储。
func NewPostgresCredentialStore(db *sql.DB, vault *credentialstore.Vault) *PostgresCredentialStore {
	return &PostgresCredentialStore{db: db, vault: vault}
}

// Rotate 加密并替换指定租户 endpoint 的当前 Provider 凭据。
func (store *PostgresCredentialStore) Rotate(ctx context.Context, tenantID, provider, endpointID, secret string) (credentialstore.Record, error) {
	if store == nil || store.db == nil {
		return credentialstore.Record{}, ErrDatabaseRequired
	}
	if store.vault == nil {
		return credentialstore.Record{}, credentialstore.ErrInvalidCredential
	}
	sealed, err := store.vault.Encrypt(tenantID, provider, endpointID, secret)
	if err != nil {
		return credentialstore.Record{}, err
	}
	id, err := credentialstore.NewID()
	if err != nil {
		return credentialstore.Record{}, err
	}
	now := time.Now().UTC()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return credentialstore.Record{}, err
	}
	defer tx.Rollback()
	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return credentialstore.Record{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE provider_credentials SET active=FALSE,revoked_at=$1 WHERE tenant_id=$2 AND provider=$3 AND endpoint_id=$4 AND active=TRUE`, now, tenantID, provider, endpointID); err != nil {
		return credentialstore.Record{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO provider_credentials (tenant_id,credential_id,provider,endpoint_id,key_version,ciphertext,active,created_at) VALUES ($1,$2,$3,$4,$5,$6,TRUE,$7)`, tenantID, id, provider, endpointID, id, sealed, now); err != nil {
		return credentialstore.Record{}, err
	}
	// 通知只用于加速刷新，数据库提交仍是凭据变更的唯一事实来源。
	_ = notifyCredentialChange(ctx, tx, CredentialChange{TenantID: tenantID, Provider: provider, EndpointID: endpointID})
	if err := tx.Commit(); err != nil {
		return credentialstore.Record{}, err
	}
	return credentialstore.Record{ID: id, TenantID: tenantID, Provider: provider, EndpointID: endpointID, KeyVersion: id, Active: true, CreatedAt: now}, nil
}

// Resolve 解密指定 endpoint 的当前 Provider 凭据。
func (store *PostgresCredentialStore) Resolve(ctx context.Context, tenantID, provider, endpointID string) ([]byte, credentialstore.Record, error) {
	if store == nil || store.db == nil {
		return nil, credentialstore.Record{}, ErrDatabaseRequired
	}
	if store.vault == nil {
		return nil, credentialstore.Record{}, credentialstore.ErrInvalidCredential
	}
	tx, err := store.beginCredentialTenantTx(ctx, tenantID)
	if err != nil {
		return nil, credentialstore.Record{}, err
	}
	defer tx.Rollback()
	var record credentialstore.Record
	var ciphertext []byte
	err = tx.QueryRowContext(ctx, `SELECT credential_id,key_version,ciphertext,created_at FROM provider_credentials WHERE tenant_id=$1 AND provider=$2 AND endpoint_id=$3 AND active=TRUE`, tenantID, provider, endpointID).Scan(&record.ID, &record.KeyVersion, &ciphertext, &record.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, credentialstore.Record{}, credentialstore.ErrNotFound
	}
	if err != nil {
		return nil, credentialstore.Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return nil, credentialstore.Record{}, err
	}
	secret, err := store.vault.Decrypt(tenantID, provider, endpointID, ciphertext)
	if err != nil {
		return nil, credentialstore.Record{}, err
	}
	record.TenantID, record.Provider, record.EndpointID, record.Active = tenantID, provider, endpointID, true
	return secret, record, nil
}

// Revoke 立即撤销指定 endpoint 的当前凭据。
func (store *PostgresCredentialStore) Revoke(ctx context.Context, tenantID, provider, endpointID string) error {
	if store == nil || store.db == nil {
		return ErrDatabaseRequired
	}
	tx, err := store.beginCredentialTenantTx(ctx, tenantID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE provider_credentials SET active=FALSE,revoked_at=CURRENT_TIMESTAMP WHERE tenant_id=$1 AND provider=$2 AND endpoint_id=$3 AND active=TRUE`, tenantID, provider, endpointID)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return credentialstore.ErrNotFound
	}
	// 通知失败不回滚撤销；其他实例可在重启时从数据库重新加载。
	_ = notifyCredentialChange(ctx, tx, CredentialChange{TenantID: tenantID, Provider: provider, EndpointID: endpointID, Revoked: true})
	return tx.Commit()
}

// beginCredentialTenantTx 开启设置了数据库租户上下文的凭据事务。
func (store *PostgresCredentialStore) beginCredentialTenantTx(ctx context.Context, tenantID string) (*sql.Tx, error) {
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

var _ credentialstore.Store = (*PostgresCredentialStore)(nil)
