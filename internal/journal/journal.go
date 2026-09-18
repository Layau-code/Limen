// Package journal 保存不含 Prompt 和 Response 的决策输入与执行计划。
package journal

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/huz/limen/internal/decision"
)

var (
	// ErrNotFound 表示指定租户下没有对应的决策记录。
	ErrNotFound = errors.New("decision not found")
	// ErrConflict 表示同一决策 ID 对应了不同内容。
	ErrConflict = errors.New("decision journal conflict")
)

// Record 是可审计的决策快照，不包含请求正文或 Provider 响应。
type Record struct {
	ID        string                 `json:"decision_id"`
	TenantID  string                 `json:"-"`
	Input     decision.Input         `json:"input"`
	Plan      decision.ExecutionPlan `json:"plan"`
	CreatedAt time.Time              `json:"created_at"`
}

// ValidateRecord 校验决策输入和计划的相互摘要，拒绝被篡改的证据。
func ValidateRecord(record Record) error {
	if record.TenantID == "" || record.ID == "" {
		return errors.New("decision record requires tenant and id")
	}
	inputHash, err := decision.HashInput(record.Input)
	if err != nil {
		return err
	}
	planHash, err := decision.HashPlan(record.Plan)
	if err != nil {
		return err
	}
	if record.Plan.InputHash != inputHash || record.Plan.PlanHash != planHash {
		return errors.New("decision record hash mismatch")
	}
	return nil
}

// Store 定义按租户保存和读取决策记录的最小接口。
type Store interface {
	Save(context.Context, Record) error
	Get(context.Context, string, string) (Record, error)
}

// MemoryStore 是单机测试和开发用的线程安全决策日志。
type MemoryStore struct {
	mu      sync.RWMutex
	records map[string]Record
}

// NewMemoryStore 创建空的内存决策日志。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: make(map[string]Record)}
}

// Save 保存一个租户决策快照，并拒绝内容冲突的重复 ID。
func (store *MemoryStore) Save(_ context.Context, record Record) error {
	if err := ValidateRecord(record); err != nil {
		return err
	}
	inputHash, _ := decision.HashInput(record.Input)
	store.mu.Lock()
	defer store.mu.Unlock()
	key := record.TenantID + "\x00" + record.ID
	if existing, ok := store.records[key]; ok {
		existingInputHash, _ := decision.HashInput(existing.Input)
		if existing.Plan.PlanHash != record.Plan.PlanHash || existingInputHash != inputHash {
			return ErrConflict
		}
		return nil
	}
	store.records[key] = record
	return nil
}

// Get 读取指定租户的决策快照，跨租户 ID 不会命中。
func (store *MemoryStore) Get(_ context.Context, tenantID, id string) (Record, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	record, ok := store.records[tenantID+"\x00"+id]
	if !ok {
		return Record{}, ErrNotFound
	}
	if err := ValidateRecord(record); err != nil {
		return Record{}, err
	}
	return record, nil
}
