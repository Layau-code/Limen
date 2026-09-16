package run

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

type MemoryStore struct {
	mu        sync.Mutex
	runs      map[string]Run
	requests  map[string]Request
	attempts  map[string]Attempt
	idem      map[string]string
	ledger    map[string]int64
	mutations map[string]mutationResult
	sequence  uint64
}

type mutationResult struct {
	hash string
	run  Run
}

// NewMemoryStore 创建用于本地开发和并发测试的强一致内存 Store。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		runs:      make(map[string]Run),
		requests:  make(map[string]Request),
		attempts:  make(map[string]Attempt),
		idem:      make(map[string]string),
		ledger:    make(map[string]int64),
		mutations: make(map[string]mutationResult),
	}
}

// RecordAttemptStarted 保存访问 Provider 前的本地 Attempt 记录。
func (store *MemoryStore) RecordAttemptStarted(tenantID string, attempt Attempt) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if tenantID == "" || attempt.ID == "" || attempt.RequestID == "" {
		return errors.New("attempt requires tenant and request")
	}
	key := resourceKey(tenantID, attempt.ID)
	if _, exists := store.attempts[key]; exists {
		return errors.New("attempt already exists")
	}
	attempt.TenantID = tenantID
	attempt.State = AttemptStarted
	store.attempts[key] = attempt
	return nil
}

// FinishAttempt 更新本地 Attempt 终态并记录完成时间。
func (store *MemoryStore) FinishAttempt(tenantID, attemptID string, state AttemptState, finishedAt time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := resourceKey(tenantID, attemptID)
	attempt, exists := store.attempts[key]
	if !exists {
		return ErrResourceNotFound
	}
	attempt.State = state
	attempt.FinishedAt = finishedAt
	store.attempts[key] = attempt
	return nil
}

// CreateRun 保存一个尚未开始请求的 Run，并拒绝跨租户 ID 冲突。
func (store *MemoryStore) CreateRun(run Run) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if run.TenantID == "" || run.ID == "" {
		return errors.New("run requires tenant_id and id")
	}
	return store.createRunLocked(run)
}

func (store *MemoryStore) createRunLocked(run Run) error {
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

// CreateRunWithMutation 以控制面幂等键创建 Run，并返回原始结果。
func (store *MemoryStore) CreateRunWithMutation(tenantID string, item Run, mutation Mutation) (Run, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := validateMutation(mutation); err != nil {
		return Run{}, err
	}
	key := mutationKey(tenantID, "create_run", mutation.Key)
	if existing, ok := store.mutations[key]; ok {
		if existing.hash != mutation.Hash {
			return existing.run, ErrIdempotencyConflict
		}
		return existing.run, nil
	}
	item.TenantID = tenantID
	if err := store.createRunLocked(item); err != nil {
		return Run{}, err
	}
	store.mutations[key] = mutationResult{hash: mutation.Hash, run: item}
	return item, nil
}

// CompleteRunWithMutation 幂等地请求结束 Run。
func (store *MemoryStore) CompleteRunWithMutation(tenantID, runID string, mutation Mutation) (Run, error) {
	return store.mutateRun(tenantID, runID, "complete_run", mutation, func(item *Run) error { return item.Complete() })
}

// CancelRunWithMutation 幂等地取消 Run。
func (store *MemoryStore) CancelRunWithMutation(tenantID, runID string, mutation Mutation) (Run, error) {
	return store.mutateRun(tenantID, runID, "cancel_run", mutation, func(item *Run) error { return item.Cancel() })
}

func (store *MemoryStore) mutateRun(tenantID, runID, operation string, mutation Mutation, change func(*Run) error) (Run, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := validateMutation(mutation); err != nil {
		return Run{}, err
	}
	key := mutationKey(tenantID, operation+"\x00"+runID, mutation.Key)
	if existing, ok := store.mutations[key]; ok {
		if existing.hash != mutation.Hash {
			return existing.run, ErrIdempotencyConflict
		}
		return existing.run, nil
	}
	runKey := resourceKey(tenantID, runID)
	item, ok := store.runs[runKey]
	if !ok {
		return Run{}, ErrResourceNotFound
	}
	if err := change(&item); err != nil {
		return item, err
	}
	store.runs[runKey] = item
	store.mutations[key] = mutationResult{hash: mutation.Hash, run: item}
	return item, nil
}

// AdmitRequest 在同一事务临界区内执行幂等检查和 Run 准入。
func (store *MemoryStore) AdmitRequest(tenantID, runID string, request Request, now time.Time) (Request, error) {
	return store.admitRequest(tenantID, runID, request, now, "", 0)
}

// AdmitRequestWithLease 在准入事务内同时写入执行实例租约。
func (store *MemoryStore) AdmitRequestWithLease(tenantID, runID string, input AdmissionInput) (Request, error) {
	return store.admitRequest(tenantID, runID, input.Request, input.Now, input.LeaseOwner, input.LeaseTTL)
}

// admitRequest 是内存 Store 的统一准入实现。
func (store *MemoryStore) admitRequest(tenantID, runID string, request Request, now time.Time, leaseOwner string, leaseTTL time.Duration) (Request, error) {
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
	if request.LeaseOwner == "" && leaseOwner != "" {
		ttl := leaseTTL
		if ttl <= 0 {
			ttl = RequestLeaseDuration
		}
		request.LeaseOwner = leaseOwner
		request.LeaseExpiresAt = now.Add(ttl)
	}
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

// AcquireRequestLease 为请求分配一个可续租的执行实例租约。
func (store *MemoryStore) AcquireRequestLease(tenantID, requestID, owner string, now time.Time, ttl time.Duration) (Request, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if owner == "" {
		return Request{}, ErrLeaseUnavailable
	}
	key := resourceKey(tenantID, requestID)
	request, ok := store.requests[key]
	if !ok {
		return Request{}, ErrResourceNotFound
	}
	if !isRequestInProgress(request.State) {
		return request, ErrRequestAlreadyProcessed
	}
	if request.LeaseOwner != "" && request.LeaseOwner != owner && now.Before(request.LeaseExpiresAt) {
		return request, ErrLeaseUnavailable
	}
	if ttl <= 0 {
		ttl = RequestLeaseDuration
	}
	request.LeaseOwner = owner
	request.LeaseExpiresAt = now.Add(ttl)
	request.UpdatedAt = now
	store.requests[key] = request
	return request, nil
}

// RenewRequestLease 延长当前执行实例持有的请求租约。
func (store *MemoryStore) RenewRequestLease(tenantID, requestID, owner string, now time.Time, ttl time.Duration) (Request, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := resourceKey(tenantID, requestID)
	request, ok := store.requests[key]
	if !ok {
		return Request{}, ErrResourceNotFound
	}
	if request.LeaseOwner != owner || request.LeaseExpiresAt.IsZero() || !now.Before(request.LeaseExpiresAt) {
		return request, ErrLeaseLost
	}
	if ttl <= 0 {
		ttl = RequestLeaseDuration
	}
	request.LeaseExpiresAt = now.Add(ttl)
	request.UpdatedAt = now
	store.requests[key] = request
	return request, nil
}

// ReleaseRequestLease 释放当前执行实例持有的请求租约。
func (store *MemoryStore) ReleaseRequestLease(tenantID, requestID, owner string, now time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := resourceKey(tenantID, requestID)
	request, ok := store.requests[key]
	if !ok {
		return ErrResourceNotFound
	}
	if request.LeaseOwner != owner {
		return ErrLeaseLost
	}
	request.LeaseOwner = ""
	request.LeaseExpiresAt = time.Time{}
	request.UpdatedAt = now
	store.requests[key] = request
	return nil
}

// RecoverExpiredRequests 将过期在途请求标记为未知费用并暂停 Run。
func (store *MemoryStore) RecoverExpiredRequests(tenantID string, now time.Time, limit int) []Request {
	store.mu.Lock()
	defer store.mu.Unlock()
	var recovered []Request
	for key, request := range store.requests {
		if tenantID != "" && request.TenantID != tenantID {
			continue
		}
		if limit > 0 && len(recovered) >= limit {
			break
		}
		if request.LeaseExpiresAt.IsZero() || now.Before(request.LeaseExpiresAt) {
			continue
		}
		if isRequestInProgress(request.State) {
			if item, ok := store.runs[resourceKey(request.TenantID, request.RunID)]; ok {
				_ = item.Settle(nil)
				store.runs[resourceKey(request.TenantID, request.RunID)] = item
			}
			request.State = RequestAbandoned
			request.SettlementStatus = "pending"
		}
		request.LeaseOwner = ""
		request.LeaseExpiresAt = time.Time{}
		request.UpdatedAt = now
		store.requests[key] = request
		recovered = append(recovered, request)
	}
	return recovered
}

// RecoverExpired 保留内存 Store 的全租户测试辅助入口。
func (store *MemoryStore) RecoverExpired(now time.Time) []Request {
	return store.RecoverExpiredRequests("", now, 0)
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

func validateMutation(mutation Mutation) error {
	if mutation.Key == "" || mutation.Hash == "" {
		return ErrIdempotencyKeyRequired
	}
	return nil
}

func mutationKey(tenantID, operation, key string) string {
	return resourceKey(tenantID, operation+"\x00"+key)
}
