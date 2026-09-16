package run

import (
	"context"
	"time"
)

// MemoryService 将内存 Store 适配到 Run Service，供单机开发和 HTTP 验证使用。
type MemoryService struct {
	store *MemoryStore
}

// NewMemoryService 创建基于指定内存 Store 的 Run Coordinator。
func NewMemoryService(store *MemoryStore) *MemoryService {
	if store == nil {
		store = NewMemoryStore()
	}
	return &MemoryService{store: store}
}

// CreateRun 在本地内存中创建一个租户 Run。
func (service *MemoryService) CreateRun(_ context.Context, tenantID string, item Run) error {
	item.TenantID = tenantID
	return service.store.CreateRun(item)
}

// CreateRunWithMutation 幂等地创建一个控制面 Run。
func (service *MemoryService) CreateRunWithMutation(_ context.Context, tenantID string, item Run, mutation Mutation) (Run, error) {
	return service.store.CreateRunWithMutation(tenantID, item, mutation)
}

// CompleteRunWithMutation 幂等地结束一个 Run。
func (service *MemoryService) CompleteRunWithMutation(_ context.Context, tenantID, runID string, mutation Mutation) (Run, error) {
	return service.store.CompleteRunWithMutation(tenantID, runID, mutation)
}

// CancelRunWithMutation 幂等地取消一个 Run。
func (service *MemoryService) CancelRunWithMutation(_ context.Context, tenantID, runID string, mutation Mutation) (Run, error) {
	return service.store.CancelRunWithMutation(tenantID, runID, mutation)
}

// AdmitRequest 执行内存 Run 的幂等准入。
func (service *MemoryService) AdmitRequest(_ context.Context, tenantID, runID string, input AdmissionInput) (Request, error) {
	return service.store.AdmitRequestWithLease(tenantID, runID, input)
}

// RecordAttemptStarted 将内存 Attempt 写入本地 Store。
func (service *MemoryService) RecordAttemptStarted(_ context.Context, tenantID string, attempt Attempt) error {
	return service.store.RecordAttemptStarted(tenantID, attempt)
}

// FinishAttempt 将内存 Attempt 更新为执行终态。
func (service *MemoryService) FinishAttempt(_ context.Context, tenantID, attemptID string, state AttemptState, finishedAt time.Time) error {
	return service.store.FinishAttempt(tenantID, attemptID, state, finishedAt)
}

// AcquireRequestLease 为内存请求分配执行实例租约。
func (service *MemoryService) AcquireRequestLease(_ context.Context, tenantID, requestID, owner string, now time.Time, ttl time.Duration) (Request, error) {
	return service.store.AcquireRequestLease(tenantID, requestID, owner, now, ttl)
}

// RenewRequestLease 延长内存请求的执行实例租约。
func (service *MemoryService) RenewRequestLease(_ context.Context, tenantID, requestID, owner string, now time.Time, ttl time.Duration) (Request, error) {
	return service.store.RenewRequestLease(tenantID, requestID, owner, now, ttl)
}

// ReleaseRequestLease 释放内存请求的执行实例租约。
func (service *MemoryService) ReleaseRequestLease(_ context.Context, tenantID, requestID, owner string, now time.Time) error {
	return service.store.ReleaseRequestLease(tenantID, requestID, owner, now)
}

// RecoverExpiredRequests 恢复指定租户的过期内存请求。
func (service *MemoryService) RecoverExpiredRequests(_ context.Context, tenantID string, now time.Time, limit int) ([]Request, error) {
	return service.store.RecoverExpiredRequests(tenantID, now, limit), nil
}

// BeginSettlement 将内存 Request 标记为待结算。
func (service *MemoryService) BeginSettlement(_ context.Context, tenantID, requestID string, now time.Time) (Request, error) {
	return service.store.BeginSettlement(tenantID, requestID, now)
}

// SettleRequest 将内存 Request 结算并释放 Run 并发名额。
func (service *MemoryService) SettleRequest(_ context.Context, tenantID, requestID string, costNanoUSD *int64, now time.Time) (Request, error) {
	return service.store.SettleRequest(tenantID, requestID, costNanoUSD, now)
}

// GetRun 读取内存 Run，并将不存在映射为统一错误。
func (service *MemoryService) GetRun(_ context.Context, tenantID, runID string) (Run, error) {
	item, ok := service.store.GetRun(tenantID, runID)
	if !ok {
		return Run{}, ErrResourceNotFound
	}
	return item, nil
}

// GetRequest 读取内存 Request，并将不存在映射为统一错误。
func (service *MemoryService) GetRequest(_ context.Context, tenantID, requestID string) (Request, error) {
	item, ok := service.store.GetRequest(tenantID, requestID)
	if !ok {
		return Request{}, ErrResourceNotFound
	}
	return item, nil
}

var _ Service = (*MemoryService)(nil)
var _ ControlService = (*MemoryService)(nil)
var _ LeaseService = (*MemoryService)(nil)
