package run

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

type MemoryStore struct {
	mu       sync.Mutex
	runs     map[string]Run
	requests map[string]Request
	idem     map[string]string
	ledger   map[string]int64
	sequence uint64
}

// NewMemoryStore 创建用于本地开发和并发测试的强一致内存 Store。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		runs:     make(map[string]Run),
		requests: make(map[string]Request),
		idem:     make(map[string]string),
		ledger:   make(map[string]int64),
	}
}

// CreateRun 保存一个尚未开始请求的 Run，并拒绝跨租户 ID 冲突。
func (store *MemoryStore) CreateRun(run Run) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if run.TenantID == "" || run.ID == "" {
		return errors.New("run requires tenant_id and id")
	}
	if run.State == "" {
		run.State = StateActive
	}
	if run.State != StateActive || run.SoftBudgetNanoUSD < 0 || run.MaxParallelism < 0 {
		return errors.New("invalid run")
	}
	key := resourceKey(run.TenantID, run.ID)
	if _, exists := store.runs[key]; exists {
		return errors.New("run already exists")
	}
	store.runs[key] = run
	return nil
}

// AdmitRequest 在同一事务临界区内执行幂等检查和 Run 准入。
func (store *MemoryStore) AdmitRequest(tenantID, runID string, request Request, now time.Time) (Request, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if request.IdempotencyKey == "" {
		return Request{}, ErrIdempotencyKeyRequired
	}
	runKey := resourceKey(tenantID, runID)
	run, exists := store.runs[runKey]
	if !exists {
		return Request{}, errors.New("run not found")
	}
	idemKey := resourceKey(tenantID, request.Endpoint+"\x00"+request.IdempotencyKey)
	if existingID, exists := store.idem[idemKey]; exists {
		existing := store.requests[resourceKey(tenantID, existingID)]
		if existing.RequestHash != request.RequestHash {
			return existing, ErrIdempotencyConflict
		}
		if isRequestInProgress(existing.State) {
			return existing, ErrRequestInProgress
		}
		return existing, ErrRequestAlreadyProcessed
	}
	if request.RequestHash == "" {
		return Request{}, errors.New("request hash is required")
	}
	if err := run.Admit(now); err != nil {
		store.runs[runKey] = run
		return Request{}, err
	}
	if request.ID == "" {
		store.sequence++
		request.ID = fmt.Sprintf("request-%d", store.sequence)
	}
	request.TenantID = tenantID
	request.RunID = runID
	request.State = RequestAdmitted
	request.CreatedAt = now
	request.UpdatedAt = now
	store.runs[runKey] = run
	store.requests[resourceKey(tenantID, request.ID)] = request
	store.idem[idemKey] = request.ID
	return request, nil
}

// BeginSettlement 将已执行请求标记为等待结算，并保留并发占用。
func (store *MemoryStore) BeginSettlement(tenantID, requestID string, now time.Time) (Request, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := resourceKey(tenantID, requestID)
	request, exists := store.requests[key]
	if !exists {
		return Request{}, errors.New("request not found")
	}
	if request.State == RequestSettled || request.State == RequestFailed || request.State == RequestCancelled || request.State == RequestAbandoned {
		return request, ErrRequestAlreadyProcessed
	}
	request.State = RequestSettlementPending
	request.SettlementStatus = "pending"
	request.UpdatedAt = now
	store.requests[key] = request
	return request, nil
}

// SettleRequest 幂等写入账本并释放 Run 并发名额。
func (store *MemoryStore) SettleRequest(tenantID, requestID string, costNanoUSD *int64, now time.Time) (Request, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	requestKey := resourceKey(tenantID, requestID)
	request, exists := store.requests[requestKey]
	if !exists {
		return Request{}, errors.New("request not found")
	}
	if request.State == RequestSettled {
		return request, nil
	}
	if request.State != RequestSettlementPending {
		return request, ErrRequestNotSettleable
	}
	runKey := resourceKey(tenantID, request.RunID)
	run, exists := store.runs[runKey]
	if !exists {
		return Request{}, errors.New("run not found")
	}
	if costNanoUSD == nil {
		request.SettlementStatus = "pending"
		request.UpdatedAt = now
		store.requests[requestKey] = request
		_ = run.Settle(nil)
		store.runs[runKey] = run
		return request, ErrAccountingSuspended
	}
	if _, recorded := store.ledger[requestKey]; !recorded {
		if err := run.Settle(costNanoUSD); err != nil {
			return request, err
		}
		store.ledger[requestKey] = *costNanoUSD
		store.runs[runKey] = run
	}
	request.State = RequestSettled
	request.SettlementStatus = "complete"
	request.LedgerRecorded = true
	request.UpdatedAt = now
	store.requests[requestKey] = request
	return request, nil
}

// GetRun 返回指定租户的 Run 副本，避免调用方绕过 Store 修改状态。
func (store *MemoryStore) GetRun(tenantID, runID string) (Run, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	run, ok := store.runs[resourceKey(tenantID, runID)]
	return run, ok
}

// GetRequest 返回指定租户的 Request 副本。
func (store *MemoryStore) GetRequest(tenantID, requestID string) (Request, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	request, ok := store.requests[resourceKey(tenantID, requestID)]
	return request, ok
}

// LedgerCount 返回测试用的唯一账本条目数量。
func (store *MemoryStore) LedgerCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.ledger)
}

// RecoverExpired 清除过期租约并返回仍需后台恢复的请求。
func (store *MemoryStore) RecoverExpired(now time.Time) []Request {
	store.mu.Lock()
	defer store.mu.Unlock()
	var recovered []Request
	for key, request := range store.requests {
		if request.LeaseExpiresAt.IsZero() || now.Before(request.LeaseExpiresAt) {
			continue
		}
		request.LeaseOwner = ""
		request.LeaseExpiresAt = time.Time{}
		request.UpdatedAt = now
		store.requests[key] = request
		recovered = append(recovered, request)
	}
	return recovered
}

func resourceKey(tenantID, resourceID string) string {
	return tenantID + "\x00" + resourceID
}

func isRequestInProgress(state RequestState) bool {
	switch state {
	case RequestAdmitted, RequestDecisionReady, RequestExecuting, RequestSettlementPending:
		return true
	default:
		return false
	}
}
