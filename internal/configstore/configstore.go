// Package configstore 保存租户隔离的不可变配置版本。
package configstore

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/huz/limen/internal/config"
)

const (
	// StateDraft 表示已经校验但尚未生效的版本。
	StateDraft = "draft"
	// StatePublished 表示当前租户正在使用的版本。
	StatePublished = "published"
	// StateSuperseded 表示曾经生效但已被新版本替换的版本。
	StateSuperseded = "superseded"
)

var (
	// ErrNotFound 表示当前租户下没有指定配置版本。
	ErrNotFound = errors.New("config version not found")
	// ErrConflict 表示相同版本标识对应了不同配置操作。
	ErrConflict = errors.New("config version conflict")
	// ErrIdempotencyConflict 表示发布幂等键对应了不同请求哈希。
	ErrIdempotencyConflict = errors.New("config publish idempotency conflict")
	// ErrApprovalNotFound 表示发布引用的审批不存在。
	ErrApprovalNotFound = errors.New("config approval not found")
	// ErrApprovalExpired 表示发布引用的审批已经过期。
	ErrApprovalExpired = errors.New("config approval expired")
	// ErrApprovalStateConflict 表示审批状态不允许消费。
	ErrApprovalStateConflict = errors.New("config approval state conflict")
	// ErrApprovalBindingConflict 表示发布与审批绑定信息不一致。
	ErrApprovalBindingConflict = errors.New("config approval binding conflict")
	// ErrApprovalActorNotDistinct 表示发布者与申请者不是两个不同身份。
	ErrApprovalActorNotDistinct = errors.New("config approval actor is not distinct")
)

// Mutation 保存配置发布的幂等键和规范请求哈希。
type Mutation struct {
	Key  string
	Hash string
}

// ApprovalBinding 保存配置发布消费审批时必须匹配的安全摘要。
type ApprovalBinding struct {
	ApprovalID            string
	PublishIdempotencyKey string
	RequestHash           string
	Publisher             string
}

// Record 是配置版本的持久化表示。
type Record struct {
	TenantID    string         `json:"-"`
	Version     string         `json:"version"`
	State       string         `json:"state"`
	Document    []byte         `json:"document,omitempty"`
	Models      []config.Model `json:"models,omitempty"`
	Routing     config.Routing `json:"routing,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	PublishedAt *time.Time     `json:"published_at,omitempty"`
}

// Store 定义配置版本的创建、查询和发布操作。
type Store interface {
	Create(context.Context, string, []byte) (Record, error)
	List(context.Context, string) ([]Record, error)
	Get(context.Context, string, string) (Record, error)
	GetPublished(context.Context, string) (Record, error)
	Publish(context.Context, string, string) (Record, error)
}

// MutationStore 为配置发布提供跨实例幂等语义。
type MutationStore interface {
	Store
	PublishWithMutation(context.Context, string, string, Mutation) (Record, error)
}

// ApprovalMutationStore 在同一事务内校验并消费审批后发布配置。
type ApprovalMutationStore interface {
	MutationStore
	PublishWithApproval(context.Context, string, string, Mutation, ApprovalBinding) (Record, error)
}

// MemoryStore 是开发环境和单元测试使用的进程内配置存储。
type MemoryStore struct {
	mu        sync.RWMutex
	records   map[string]Record
	active    map[string]string
	mutations map[string]publishMutation
	now       func() time.Time
}

type publishMutation struct {
	hash   string
	record Record
}

// NewMemoryStore 创建空的内存配置存储。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: make(map[string]Record), active: make(map[string]string), mutations: make(map[string]publishMutation), now: time.Now}
}

// Create 校验并保存一个不可变配置版本，重复提交相同版本保持幂等。
func (store *MemoryStore) Create(ctx context.Context, tenantID string, document []byte) (Record, error) {
	if err := contextError(ctx); err != nil {
		return Record{}, err
	}
	version, models, routing, err := parse(document)
	if err != nil {
		return Record{}, err
	}
	if strings.TrimSpace(tenantID) == "" {
		return Record{}, errors.New("tenant id is required")
	}
	key := recordKey(tenantID, version)
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.records[key]; ok {
		return cloneRecord(existing), nil
	}
	record := Record{TenantID: tenantID, Version: version, State: StateDraft, Document: append([]byte(nil), document...), Models: models, Routing: routing, CreatedAt: store.now().UTC()}
	store.records[key] = cloneRecord(record)
	return cloneRecord(record), nil
}

// List 返回当前租户的版本，按创建时间倒序排列。
func (store *MemoryStore) List(ctx context.Context, tenantID string) ([]Record, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make([]Record, 0)
	for _, record := range store.records {
		if record.TenantID == tenantID {
			result = append(result, cloneRecord(record))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].Version > result[j].Version
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result, nil
}

// Get 返回指定租户的单个配置版本。
func (store *MemoryStore) Get(ctx context.Context, tenantID, version string) (Record, error) {
	if err := contextError(ctx); err != nil {
		return Record{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	record, ok := store.records[recordKey(tenantID, version)]
	if !ok {
		return Record{}, ErrNotFound
	}
	return cloneRecord(record), nil
}

// GetPublished 返回当前租户唯一的已发布版本。
func (store *MemoryStore) GetPublished(ctx context.Context, tenantID string) (Record, error) {
	if err := contextError(ctx); err != nil {
		return Record{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	version := store.active[tenantID]
	if version == "" {
		return Record{}, ErrNotFound
	}
	record, ok := store.records[recordKey(tenantID, version)]
	if !ok {
		return Record{}, ErrNotFound
	}
	return cloneRecord(record), nil
}

// Publish 将一个草稿设为当前版本，并把旧版本标记为已替代。
func (store *MemoryStore) Publish(ctx context.Context, tenantID, version string) (Record, error) {
	return store.publish(ctx, tenantID, version, nil)
}

// PublishWithMutation 幂等发布配置版本并返回首次发布结果。
func (store *MemoryStore) PublishWithMutation(ctx context.Context, tenantID, version string, mutation Mutation) (Record, error) {
	if strings.TrimSpace(mutation.Key) == "" || strings.TrimSpace(mutation.Hash) == "" {
		return Record{}, ErrIdempotencyConflict
	}
	return store.publish(ctx, tenantID, version, &mutation)
}

// publish 在同一内存临界区内完成配置切换和幂等记录。
func (store *MemoryStore) publish(ctx context.Context, tenantID, version string, mutation *Mutation) (Record, error) {
	if err := contextError(ctx); err != nil {
		return Record{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if mutation != nil {
		key := recordKey(tenantID, mutation.Key)
		if existing, ok := store.mutations[key]; ok {
			if existing.hash != mutation.Hash {
				return existing.record, ErrIdempotencyConflict
			}
			return cloneRecord(existing.record), nil
		}
	}
	key := recordKey(tenantID, version)
	record, ok := store.records[key]
	if !ok {
		return Record{}, ErrNotFound
	}
	if oldVersion := store.active[tenantID]; oldVersion != "" && oldVersion != version {
		oldKey := recordKey(tenantID, oldVersion)
		old := store.records[oldKey]
		old.State = StateSuperseded
		store.records[oldKey] = old
	}
	now := store.now().UTC()
	record.State = StatePublished
	record.PublishedAt = &now
	store.records[key] = record
	store.active[tenantID] = version
	if mutation != nil {
		store.mutations[recordKey(tenantID, mutation.Key)] = publishMutation{hash: mutation.Hash, record: record}
	}
	return cloneRecord(record), nil
}

// parse 将原始 JSON 转换为版本哈希和可用于构建 Router 的配置。
func parse(document []byte) (string, []config.Model, config.Routing, error) {
	models, routing, version, err := config.ParseModels(document, config.DefaultRouting())
	if err != nil {
		return "", nil, config.Routing{}, err
	}
	return version, models, routing, nil
}

// cloneRecord 防止调用方修改存储内部的字节、切片和时间指针。
func cloneRecord(record Record) Record {
	record.Document = append([]byte(nil), record.Document...)
	record.Models = append([]config.Model(nil), record.Models...)
	for i := range record.Models {
		record.Models[i].Targets = append([]config.Target(nil), record.Models[i].Targets...)
		for j := range record.Models[i].Targets {
			target := &record.Models[i].Targets[j]
			target.Capabilities = append([]string(nil), target.Capabilities...)
			target.DataClasses = append([]string(nil), target.DataClasses...)
			if target.Pricing != nil {
				pricing := *target.Pricing
				target.Pricing = &pricing
			}
		}
	}
	if record.PublishedAt != nil {
		publishedAt := *record.PublishedAt
		record.PublishedAt = &publishedAt
	}
	return record
}

// contextError 在内存实现中保持与数据库实现一致的取消语义。
func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func recordKey(tenantID, version string) string {
	return tenantID + "\x00" + version
}
