package run

import (
	"context"
	"errors"
	"time"
)

var settlementRetryBackoff = []time.Duration{
	100 * time.Millisecond,
	500 * time.Millisecond,
	2 * time.Second,
	10 * time.Second,
}

// ProcessSettlementJobs 领取并幂等处理一批持久化结算任务。
func ProcessSettlementJobs(ctx context.Context, service SettlementRecoveryService, tenantID, owner string, now time.Time, limit int) (int, error) {
	if service == nil {
		return 0, errors.New("settlement recovery service is unavailable")
	}
	jobs, err := service.ClaimSettlementJobs(ctx, tenantID, owner, now, RequestLeaseDuration, limit)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, job := range jobs {
		if processSettlementJob(ctx, service, job, owner, now) {
			processed++
		}
	}
	return processed, nil
}

// processSettlementJob 处理单条任务，失败时释放租约并安排退避重试。
func processSettlementJob(ctx context.Context, service SettlementRecoveryService, job SettlementJob, owner string, now time.Time) bool {
	operationContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := service.BeginSettlement(operationContext, job.TenantID, job.RequestID, now); err != nil && !errors.Is(err, ErrRequestAlreadyProcessed) {
		if errors.Is(err, ErrRequestNotSettleable) || errors.Is(err, ErrResourceNotFound) {
			return service.CompleteSettlementJob(operationContext, job.TenantID, job.RequestID, owner) == nil
		}
		return failSettlementJob(operationContext, service, job, owner, now, err)
	}
	_, err := service.SettleRequest(operationContext, job.TenantID, job.RequestID, job.CostNanoUSD, now)
	if err == nil || errors.Is(err, ErrAccountingSuspended) || errors.Is(err, ErrRequestAlreadyProcessed) || errors.Is(err, ErrRequestNotSettleable) {
		return service.CompleteSettlementJob(operationContext, job.TenantID, job.RequestID, owner) == nil
	}
	return failSettlementJob(operationContext, service, job, owner, now, err)
}

// failSettlementJob 以稳定原因码记录失败，不把底层错误写入持久化任务。
func failSettlementJob(ctx context.Context, service SettlementRecoveryService, job SettlementJob, owner string, now time.Time, _ error) bool {
	index := job.Attempts - 1
	if index < 0 {
		index = 0
	}
	if index >= len(settlementRetryBackoff) {
		index = len(settlementRetryBackoff) - 1
	}
	return service.FailSettlementJob(ctx, job.TenantID, job.RequestID, owner, now.Add(settlementRetryBackoff[index]), "settlement_retry_failed") == nil
}
