// Package approval 定义配置发布的可选双人审批状态机。
package approval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

const (
	// StatePending 表示审批已经申请但尚未作出决定。
	StatePending = "pending"
	// StateApproved 表示审批者已经批准对应发布操作。
	StateApproved = "approved"
	// StateConsumed 表示批准已经被一次发布操作消费。
	StateConsumed = "consumed"
	// StateRejected 表示审批者拒绝了对应发布操作。
	StateRejected = "rejected"
	// StateExpired 表示审批在等待决定时超过了有效期。
	StateExpired = "expired"
)

// DefaultTTL 定义审批在未作出决定时的默认有效期。
const DefaultTTL = 30 * time.Minute

var (
	// ErrNotFound 表示当前租户下不存在指定审批。
	ErrNotFound = errors.New("approval not found")
	// ErrIdempotencyConflict 表示操作幂等键对应了不同请求。
	ErrIdempotencyConflict = errors.New("approval idempotency conflict")
	// ErrBindingConflict 表示审批绑定的版本、发布键或请求哈希不一致。
	ErrBindingConflict = errors.New("approval binding conflict")
	// ErrActorNotDistinct 表示审批者与申请者不是两个不同身份。
	ErrActorNotDistinct = errors.New("approval actor is not distinct")
	// ErrStateConflict 表示当前状态不允许执行目标操作。
	ErrStateConflict = errors.New("approval state conflict")
	// ErrExpired 表示审批已经过期，不能继续批准、拒绝或消费。
	ErrExpired = errors.New("approval expired")
	// ErrInvalid 表示审批输入缺少必要字段。
	ErrInvalid = errors.New("invalid approval")
)

// Mutation 保存审批控制面操作的幂等键和规范请求哈希。
type Mutation struct {
	Key  string
	Hash string
}

// Binding 描述配置发布消费审批时必须匹配的不可变绑定信息。
type Binding struct {
	TenantID              string
	ConfigVersion         string
	ApprovalID            string
	PublishIdempotencyKey string
	RequestHash           string
	Publisher             string
}

// Record 是审批状态机的持久化表示，不包含配置正文或凭据。
type Record struct {
	TenantID              string    `json:"-"`
	ID                    string    `json:"approval_id"`
	ConfigVersion         string    `json:"config_version"`
	PublishIdempotencyKey string    `json:"publish_idempotency_key"`
	RequestHash           string    `json:"request_hash"`
	RequestedBy           string    `json:"requested_by"`
	ApprovedBy            string    `json:"approved_by,omitempty"`
	State                 string    `json:"state"`
	ExpiresAt             time.Time `json:"expires_at"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// Store 定义审批申请、审批决定和发布消费的持久化边界。
type Store interface {
	Create(context.Context, string, string, string, string, string, Mutation) (Record, error)
	Get(context.Context, string, string, string) (Record, error)
	Approve(context.Context, string, string, string, string, Mutation) (Record, error)
	Reject(context.Context, string, string, string, string, Mutation) (Record, error)
	ValidateForPublish(context.Context, Binding) (Record, error)
	Consume(context.Context, Binding) (Record, error)
}

// MemoryStore 是单机开发和测试使用的进程内审批存储。
type MemoryStore struct {
	mu        sync.Mutex
	records   map[string]Record
	bindings  map[string]string
	mutations map[string]approvalMutation
	now       func() time.Time
}

type approvalMutation struct {
	hash       string
	approvalID string
}

// NewMemoryStore 创建使用系统时钟的内存审批存储。
func NewMemoryStore() *MemoryStore {
	return newMemoryStore(time.Now)
}

func newMemoryStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{
		records:   make(map[string]Record),
		bindings:  make(map[string]string),
		mutations: make(map[string]approvalMutation),
		now:       now,
	}
}

// Create 创建绑定到配置版本和发布幂等键的待审批记录。
func (store *MemoryStore) Create(ctx context.Context, tenantID, configVersion, publishKey, requestHash, requestedBy string, mutation Mutation) (Record, error) {
	if err := contextError(ctx); err != nil {
		return Record{}, err
	}
	if err := validateCreate(tenantID, configVersion, publishKey, requestHash, requestedBy, mutation); err != nil {
		return Record{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	operation := operationKey(tenantID, "create", mutation.Key)
	if existing, ok := store.mutations[operation]; ok {
		if existing.hash != mutation.Hash {
			return Record{}, ErrIdempotencyConflict
		}
		return store.getLocked(tenantID, configVersion, existing.approvalID)
	}
	binding := bindingKey(tenantID, configVersion, publishKey)
	if _, ok := store.bindings[binding]; ok {
		return Record{}, ErrBindingConflict
	}
	now := store.now().UTC()
	id, err := newID()
	if err != nil {
		return Record{}, err
	}
	record := Record{
		TenantID:              tenantID,
		ID:                    id,
		ConfigVersion:         configVersion,
		PublishIdempotencyKey: publishKey,
		RequestHash:           requestHash,
		RequestedBy:           requestedBy,
		State:                 StatePending,
		ExpiresAt:             now.Add(DefaultTTL),
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	store.records[recordKey(tenantID, configVersion, id)] = record
	store.bindings[binding] = id
	store.mutations[operation] = approvalMutation{hash: mutation.Hash, approvalID: id}
	return clone(record), nil
}

// Get 返回审批状态，并在读取时将已过期的待审批记录标记为 expired。
func (store *MemoryStore) Get(ctx context.Context, tenantID, configVersion, approvalID string) (Record, error) {
	if err := contextError(ctx); err != nil {
		return Record{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.getLocked(tenantID, configVersion, approvalID)
}

// Approve 由不同于申请者的身份批准待审批发布。
func (store *MemoryStore) Approve(ctx context.Context, tenantID, configVersion, approvalID, actorID string, mutation Mutation) (Record, error) {
	return store.decide(ctx, tenantID, configVersion, approvalID, actorID, mutation, true)
}

// Reject 由不同于申请者的身份拒绝待审批发布。
func (store *MemoryStore) Reject(ctx context.Context, tenantID, configVersion, approvalID, actorID string, mutation Mutation) (Record, error) {
	return store.decide(ctx, tenantID, configVersion, approvalID, actorID, mutation, false)
}

// ValidateForPublish 检查发布请求是否仍绑定到可消费的审批。
func (store *MemoryStore) ValidateForPublish(ctx context.Context, binding Binding) (Record, error) {
	if err := contextError(ctx); err != nil {
		return Record{}, err
	}
	if err := validateBinding(binding); err != nil {
		return Record{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record, err := store.getLocked(binding.TenantID, binding.ConfigVersion, binding.ApprovalID)
	if err != nil {
		return Record{}, err
	}
	if err := checkBinding(record, binding); err != nil {
		return Record{}, err
	}
	if record.State != StateApproved && record.State != StateConsumed {
		return Record{}, stateError(record.State)
	}
	return record, nil
}

// Consume 将已批准的审批绑定到一次发布，并允许同一发布重试读取 consumed 状态。
func (store *MemoryStore) Consume(ctx context.Context, binding Binding) (Record, error) {
	if err := contextError(ctx); err != nil {
		return Record{}, err
	}
	if err := validateBinding(binding); err != nil {
		return Record{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record, err := store.getLocked(binding.TenantID, binding.ConfigVersion, binding.ApprovalID)
	if err != nil {
		return Record{}, err
	}
	if err := checkBinding(record, binding); err != nil {
		return Record{}, err
	}
	if record.State == StateConsumed {
		return record, nil
	}
	if record.State != StateApproved {
		return Record{}, stateError(record.State)
	}
	now := store.now().UTC()
	record.State = StateConsumed
	record.UpdatedAt = now
	store.records[recordKey(record.TenantID, record.ConfigVersion, record.ID)] = record
	return clone(record), nil
}

// decide 在内存临界区内执行批准或拒绝，并记录操作幂等结果。
func (store *MemoryStore) decide(ctx context.Context, tenantID, configVersion, approvalID, actorID string, mutation Mutation, approve bool) (Record, error) {
	if err := contextError(ctx); err != nil {
		return Record{}, err
	}
	if strings.TrimSpace(actorID) == "" || strings.TrimSpace(mutation.Key) == "" || strings.TrimSpace(mutation.Hash) == "" {
		return Record{}, ErrInvalid
	}
	operationName := "reject"
	if approve {
		operationName = "approve"
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	operation := operationKey(tenantID, operationName+":"+approvalID, mutation.Key)
	if existing, ok := store.mutations[operation]; ok {
		if existing.hash != mutation.Hash {
			return Record{}, ErrIdempotencyConflict
		}
		return store.getLocked(tenantID, configVersion, existing.approvalID)
	}
	record, err := store.getLocked(tenantID, configVersion, approvalID)
	if err != nil {
		return Record{}, err
	}
	if record.State == StateExpired {
		return Record{}, ErrExpired
	}
	if record.State != StatePending {
		return Record{}, stateError(record.State)
	}
	if actorID == record.RequestedBy {
		return Record{}, ErrActorNotDistinct
	}
	now := store.now().UTC()
	if approve {
		record.State = StateApproved
		record.ApprovedBy = actorID
	} else {
		record.State = StateRejected
	}
	record.UpdatedAt = now
	store.records[recordKey(record.TenantID, record.ConfigVersion, record.ID)] = record
	store.mutations[operation] = approvalMutation{hash: mutation.Hash, approvalID: record.ID}
	return clone(record), nil
}

func (store *MemoryStore) getLocked(tenantID, configVersion, approvalID string) (Record, error) {
	key := recordKey(tenantID, configVersion, approvalID)
	record, ok := store.records[key]
	if !ok {
		return Record{}, ErrNotFound
	}
	if record.State == StatePending && !store.now().Before(record.ExpiresAt) {
		record.State = StateExpired
		record.UpdatedAt = store.now().UTC()
		store.records[key] = record
	}
	return clone(record), nil
}

func validateCreate(tenantID, configVersion, publishKey, requestHash, requestedBy string, mutation Mutation) error {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(configVersion) == "" || strings.TrimSpace(publishKey) == "" || strings.TrimSpace(requestHash) == "" || strings.TrimSpace(requestedBy) == "" || strings.TrimSpace(mutation.Key) == "" || strings.TrimSpace(mutation.Hash) == "" {
		return ErrInvalid
	}
	return nil
}

func validateBinding(binding Binding) error {
	if strings.TrimSpace(binding.TenantID) == "" || strings.TrimSpace(binding.ConfigVersion) == "" || strings.TrimSpace(binding.ApprovalID) == "" || strings.TrimSpace(binding.PublishIdempotencyKey) == "" || strings.TrimSpace(binding.RequestHash) == "" || strings.TrimSpace(binding.Publisher) == "" {
		return ErrInvalid
	}
	return nil
}

func checkBinding(record Record, binding Binding) error {
	if record.TenantID != binding.TenantID || record.ConfigVersion != binding.ConfigVersion || record.ID != binding.ApprovalID || record.PublishIdempotencyKey != binding.PublishIdempotencyKey || record.RequestHash != binding.RequestHash {
		return ErrBindingConflict
	}
	if record.RequestedBy == binding.Publisher {
		return ErrActorNotDistinct
	}
	return nil
}

func stateError(state string) error {
	if state == StateExpired {
		return ErrExpired
	}
	return ErrStateConflict
}

func newID() (string, error) {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "approval_" + hex.EncodeToString(value[:]), nil
}

// NewID 生成不包含业务正文的审批标识，供持久化实现创建记录。
func NewID() (string, error) {
	return newID()
}

func recordKey(tenantID, configVersion, approvalID string) string {
	return tenantID + "\x00" + configVersion + "\x00" + approvalID
}

func bindingKey(tenantID, configVersion, publishKey string) string {
	return tenantID + "\x00" + configVersion + "\x00" + publishKey
}

func operationKey(tenantID, operation, key string) string {
	return tenantID + "\x00" + operation + "\x00" + key
}

func clone(record Record) Record {
	return record
}

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
