// Package store 定义 Limen 受治理状态的强一致持久化边界。
package store

import (
	"context"
	"errors"
	"time"

	"github.com/huz/limen/internal/run"
)

var (
	// ErrIdempotencyConflict 表示同一幂等键对应了不同请求哈希。
	ErrIdempotencyConflict = run.ErrIdempotencyConflict
	// ErrRequestInProgress 表示同一请求仍在执行或等待结算。
	ErrRequestInProgress = run.ErrRequestInProgress
	// ErrRequestAlreadyProcessed 表示同一请求已经完成处理。
	ErrRequestAlreadyProcessed = run.ErrRequestAlreadyProcessed
	// ErrRunNotActive 表示 Run 当前不接受新请求。
	ErrRunNotActive = run.ErrRunNotActive
	// ErrAccountingSuspended 表示账本结果不确定，必须先恢复对账。
	ErrAccountingSuspended = run.ErrAccountingSuspended
	// ErrNotFound 表示指定租户下资源不存在。
	ErrNotFound = errors.New("store resource not found")
)

// AdmissionInput 描述一次已完成哈希的请求准入。
type AdmissionInput struct {
	Request run.Request
	Now     time.Time
}

// Store 定义 Run、Request、Attempt 和 Ledger 的租户显式操作。
type Store interface {
	CreateRun(context.Context, string, run.Run) error
	AdmitRequest(context.Context, string, string, AdmissionInput) (run.Request, error)
	RecordAttemptStarted(context.Context, string, run.Attempt) error
	BeginSettlement(context.Context, string, string, time.Time) (run.Request, error)
	SettleRequest(context.Context, string, string, *int64, time.Time) (run.Request, error)
	GetRun(context.Context, string, string) (run.Run, error)
	GetRequest(context.Context, string, string) (run.Request, error)
}
