package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/huz/limen/internal/run"
	"github.com/lib/pq"
)

const cancellationEventChannel = "limen_cancellation_events"

// CancellationEventListener 接收 PostgreSQL Run 取消通知并广播给本地请求。
type CancellationEventListener struct {
	listener *pq.Listener
}

// NewCancellationEventListener 创建带自动重连能力的取消通知监听器。
func NewCancellationEventListener(databaseURL string) (*CancellationEventListener, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, errors.New("database URL is required")
	}
	return &CancellationEventListener{listener: pq.NewListener(databaseURL, 10*time.Second, 30*time.Second, nil)}, nil
}

// Run 订阅取消事件；事件表轮询负责通知丢失时的最终恢复。
func (listener *CancellationEventListener) Run(ctx context.Context, handle func(run.CancellationEvent)) error {
	if listener == nil || listener.listener == nil {
		return errors.New("cancellation listener is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := listener.listener.Listen(cancellationEventChannel); err != nil {
		return err
	}
	for {
		select {
		case notification, ok := <-listener.listener.Notify:
			if !ok || notification == nil || handle == nil {
				if !ok {
					return errors.New("cancellation listener closed")
				}
				continue
			}
			event, err := parseCancellationEvent(notification.Extra)
			if err == nil {
				handle(event)
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// Close 关闭取消通知连接。
func (listener *CancellationEventListener) Close() error {
	if listener == nil || listener.listener == nil {
		return nil
	}
	return listener.listener.Close()
}

// parseCancellationEvent 校验通知中的租户、Run 和事件 ID 元数据。
func parseCancellationEvent(raw string) (run.CancellationEvent, error) {
	var event run.CancellationEvent
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		return run.CancellationEvent{}, err
	}
	if event.ID <= 0 || event.TenantID == "" || event.RunID == "" {
		return run.CancellationEvent{}, errors.New("cancellation event is incomplete")
	}
	return event, nil
}

// notifyCancellationEvent 在取消事务中写入提交后可见的元数据通知。
func notifyCancellationEvent(ctx context.Context, tx *sql.Tx, event run.CancellationEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "SELECT pg_notify($1,$2)", cancellationEventChannel, string(payload))
	return err
}
