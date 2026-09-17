package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/lib/pq"
)

const credentialChangeChannel = "limen_credential_changes"

// CredentialChange 描述一次不包含密钥的 Provider 凭据变更通知。
type CredentialChange struct {
	TenantID   string `json:"tenant_id"`
	Provider   string `json:"provider"`
	EndpointID string `json:"endpoint_id"`
	Revoked    bool   `json:"revoked"`
}

// CredentialChangeListener 接收 PostgreSQL 凭据变更并交给内存 Provider 刷新。
type CredentialChangeListener struct {
	listener *pq.Listener
}

// NewCredentialChangeListener 创建带自动重连能力的 PostgreSQL 通知监听器。
func NewCredentialChangeListener(databaseURL string) (*CredentialChangeListener, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, errors.New("database URL is required")
	}
	return &CredentialChangeListener{listener: pq.NewListener(databaseURL, 10*time.Second, 30*time.Second, nil)}, nil
}

// Run 订阅凭据变更通知；通知只用于加速刷新，数据库仍是唯一事实来源。
func (listener *CredentialChangeListener) Run(ctx context.Context, handle func(CredentialChange)) error {
	if listener == nil || listener.listener == nil {
		return errors.New("credential listener is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := listener.listener.Listen(credentialChangeChannel); err != nil {
		return err
	}
	for {
		select {
		case notification, ok := <-listener.listener.Notify:
			if !ok {
				return errors.New("credential listener closed")
			}
			if notification == nil || handle == nil {
				continue
			}
			change, err := parseCredentialChange(notification.Extra)
			if err != nil {
				continue
			}
			handle(change)
		case <-ctx.Done():
			return nil
		}
	}
}

// parseCredentialChange 校验通知中的租户、Provider 和 endpoint 元数据。
func parseCredentialChange(raw string) (CredentialChange, error) {
	var change CredentialChange
	if err := json.Unmarshal([]byte(raw), &change); err != nil {
		return CredentialChange{}, err
	}
	if change.TenantID == "" || change.Provider == "" || change.EndpointID == "" {
		return CredentialChange{}, errors.New("credential change is incomplete")
	}
	return change, nil
}

// Close 关闭 PostgreSQL 通知连接。
func (listener *CredentialChangeListener) Close() error {
	if listener == nil || listener.listener == nil {
		return nil
	}
	return listener.listener.Close()
}

// notifyCredentialChange 在凭据事务中写入提交后可见的变更通知。
func notifyCredentialChange(ctx context.Context, tx *sql.Tx, change CredentialChange) error {
	payload, err := json.Marshal(change)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "SELECT pg_notify($1,$2)", credentialChangeChannel, string(payload))
	return err
}
