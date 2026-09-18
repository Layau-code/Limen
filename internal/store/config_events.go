package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/lib/pq"
)

const configChangeChannel = "limen_config_changes"

// ConfigChange 描述一次不包含配置正文的版本变更通知。
type ConfigChange struct {
	TenantID string `json:"tenant_id"`
	Version  string `json:"version"`
}

// ConfigChangeListener 接收 PostgreSQL 配置版本变更并刷新本地 Router。
type ConfigChangeListener struct {
	listener *pq.Listener
}

// NewConfigChangeListener 创建带自动重连能力的配置变更监听器。
func NewConfigChangeListener(databaseURL string) (*ConfigChangeListener, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, errors.New("database URL is required")
	}
	return &ConfigChangeListener{listener: pq.NewListener(databaseURL, 10*time.Second, 30*time.Second, nil)}, nil
}

// Run 订阅配置变更通知；调用方应保留轮询作为通知丢失时的最终恢复。
func (listener *ConfigChangeListener) Run(ctx context.Context, handle func(ConfigChange)) error {
	if listener == nil || listener.listener == nil {
		return errors.New("config listener is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := listener.listener.Listen(configChangeChannel); err != nil {
		return err
	}
	for {
		select {
		case notification, ok := <-listener.listener.Notify:
			if !ok {
				return errors.New("config listener closed")
			}
			if notification == nil || handle == nil {
				continue
			}
			change, err := parseConfigChange(notification.Extra)
			if err == nil {
				handle(change)
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// parseConfigChange 只接受租户和版本哈希，拒绝正文及未知字段。
func parseConfigChange(raw string) (ConfigChange, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var change ConfigChange
	if err := decoder.Decode(&change); err != nil {
		return ConfigChange{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return ConfigChange{}, errors.New("config change contains multiple JSON values")
		}
		return ConfigChange{}, err
	}
	change.TenantID = strings.TrimSpace(change.TenantID)
	change.Version = strings.TrimSpace(change.Version)
	if change.TenantID == "" || change.Version == "" {
		return ConfigChange{}, errors.New("config change is incomplete")
	}
	return change, nil
}

// Close 关闭 PostgreSQL 配置通知连接。
func (listener *ConfigChangeListener) Close() error {
	if listener == nil || listener.listener == nil {
		return nil
	}
	return listener.listener.Close()
}

// notifyConfigChange 在配置发布事务中写入提交后可见的元数据通知。
func notifyConfigChange(ctx context.Context, tx *sql.Tx, change ConfigChange) error {
	payload, err := json.Marshal(change)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "SELECT pg_notify($1,$2)", configChangeChannel, string(payload))
	return err
}
