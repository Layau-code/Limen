// Package audit 保存控制面变更的安全摘要，便于定位配置和运行治理操作。
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	// ErrInvalidEvent 表示审计事件缺少必要的安全摘要字段。
	ErrInvalidEvent = errors.New("invalid audit event")
	// ErrNotFound 表示租户没有对应的审计事件。
	ErrNotFound = errors.New("audit event not found")
)

const (
	ActionConfigCreate      = "config.create"
	ActionConfigPublish     = "config.publish"
	ActionApprovalRequested = "config.approval.requested"
	ActionApprovalApproved  = "config.approval.approved"
	ActionApprovalRejected  = "config.approval.rejected"
	ActionApprovalConsumed  = "config.approval.consumed"
	ActionAPIKeyCreate      = "api_key.create"
	ActionAPIKeyRotate      = "api_key.rotate"
	ActionAPIKeyRevoke      = "api_key.revoke"
	ActionCredentialRotate  = "credential.rotate"
	ActionCredentialRevoke  = "credential.revoke"
	ActionRunComplete       = "run.complete"
	ActionRunCancel         = "run.cancel"
	ActionAccountingResolve = "accounting.resolve"
)

// Event 是不包含请求正文、响应正文和凭据的管理操作摘要。
type Event struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"-"`
	ActorID      string    `json:"actor_id,omitempty"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
	Outcome      string    `json:"outcome"`
	RequestHash  string    `json:"request_hash,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// EventID 为同一租户、操作和资源生成稳定 ID，便于控制面重试去重。
func EventID(tenantID, action, resourceID, requestHash string) string {
	return EventIDWithActor(tenantID, "", action, resourceID, requestHash)
}

// EventIDWithActor 为包含执行者身份的审计事件生成稳定 ID。
func EventIDWithActor(tenantID, actorID, action, resourceID, requestHash string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{tenantID, actorID, action, resourceID, requestHash}, "\x00")))
	return "audit_" + hex.EncodeToString(sum[:12])
}

// Store 定义租户隔离的审计追加和查询边界。
type Store interface {
	Append(context.Context, Event) error
	List(context.Context, string, int) ([]Event, error)
}

// Validate 检查审计事件字段，避免把动态正文写入控制面记录。
func (event Event) Validate() error {
	if strings.TrimSpace(event.TenantID) == "" || strings.TrimSpace(event.ID) == "" || strings.TrimSpace(event.Action) == "" || strings.TrimSpace(event.ResourceType) == "" || strings.TrimSpace(event.ResourceID) == "" || strings.TrimSpace(event.Outcome) == "" || event.CreatedAt.IsZero() {
		return ErrInvalidEvent
	}
	return nil
}

// MemoryStore 是单机开发和测试使用的有界审计存储。
type MemoryStore struct {
	mu     sync.RWMutex
	limit  int
	events []Event
}

// NewMemoryStore 创建一个保留最近事件的内存审计存储。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{limit: 10000, events: make([]Event, 0, 64)}
}

// Append 追加一个事件并按固定上限淘汰最旧记录。
func (store *MemoryStore) Append(ctx context.Context, event Event) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := event.Validate(); err != nil {
		return err
	}
	if store == nil {
		return ErrInvalidEvent
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, existing := range store.events {
		if existing.TenantID == event.TenantID && existing.ID == event.ID {
			if existing.Action != event.Action || existing.ResourceID != event.ResourceID || existing.RequestHash != event.RequestHash {
				return ErrInvalidEvent
			}
			return nil
		}
	}
	store.events = append(store.events, cloneEvent(event))
	if len(store.events) > store.limit {
		store.events = append([]Event(nil), store.events[len(store.events)-store.limit:]...)
	}
	return nil
}

// List 返回租户最近的审计事件，结果按创建时间倒序排列。
func (store *MemoryStore) List(ctx context.Context, tenantID string, limit int) ([]Event, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if store == nil || strings.TrimSpace(tenantID) == "" || limit <= 0 {
		return nil, ErrInvalidEvent
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make([]Event, 0, limit)
	for index := len(store.events) - 1; index >= 0 && len(result) < limit; index-- {
		if store.events[index].TenantID == tenantID {
			result = append(result, cloneEvent(store.events[index]))
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID > result[j].ID
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result, nil
}

// contextError 让内存实现与持久化实现保持取消语义一致。
func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// cloneEvent 避免调用方修改已经保存的审计摘要。
func cloneEvent(event Event) Event {
	return event
}
