# Limen 阶段 B：Run 与可信账本边界实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 建立 Run 级预算、并发、幂等和结算状态机，并用可替换的 Store 接口和 PostgreSQL 迁移固定强一致边界。

**Architecture:** `internal/run` 只保存可测试的领域状态和状态转移；`internal/store` 提供租户隔离的持久化接口与 SQL 迁移；阶段 B 第一批使用线程安全 MemoryStore 验证并发和幂等，Gateway 仍保持无 Run 兼容路径。任何请求费用只在结束后结算，Run 的 soft budget 是后续请求准入阈值，不是单请求硬上限。

**Tech Stack:** Go 标准库、`database/sql` 接口、PostgreSQL SQL 迁移；不在本阶段引入 Web 框架或 ORM。

> 状态：领域状态机、幂等 MemoryStore、PostgreSQL 迁移/Repository、单机 HTTP Run 控制面、受治理 Chat 准入/结算、请求租约恢复和可选 PostgreSQL 启动装配已完成；跨实例取消、数据库集成测试和多租户 API Key 控制面属于下一阶段。

---

### Task 1：定义 Run、Request、Attempt 状态机

**Files:** 创建 `internal/run/types.go`、`internal/run/state.go`；测试 `internal/run/state_test.go`。

- [ ] **Step 1: 写失败测试**

覆盖 active → completing → completed、cancel、deadline、soft budget、suspended accounting；验证 completing 和 suspended accounting 不再准入，settlement_pending 仍占用并发名额，重复终态转移被拒绝。

- [ ] **Step 2: 运行失败测试**

运行 `go test ./internal/run -run 'TestRun|TestRequest' -count=1`，预期因类型和状态函数尚不存在而失败。

- [ ] **Step 3: 实现领域类型和纯状态转移**

定义 `RunState`、`RequestState`、`AttemptState` 常量和 `Run`、`Request`、`Attempt` 结构；实现 `Run.Admit(now)`、`Run.Complete()`、`Run.Cancel()`、`Run.MarkDeadlineExceeded()`、`Run.MarkBudgetExhausted()`、`Run.SuspendAccounting()`、`Run.Settle(cost)`。`Admit` 只检查 active、deadline、已结算成本和 `InFlight < MaxParallelism`，不检查单请求预计费用。

- [ ] **Step 4: 运行测试并提交**

运行 `gofmt -w internal/run`、`go test ./internal/run -count=1`，再提交 `git add internal/run && git commit -m "feat: add Run state machine"`。

### Task 2：实现幂等哈希和线程安全 MemoryStore

**Files:** 创建 `internal/run/idempotency.go`、`internal/run/memory.go`；测试 `internal/run/idempotency_test.go`、`internal/run/memory_test.go`。

- [ ] **Step 1: 写失败测试**

使用 100 个并发调用验证同一 tenant、endpoint、Idempotency-Key 至多创建一个 Request；相同规范请求返回 `request_in_progress`，不同请求返回 `idempotency_conflict`；结算重复提交不能新增 Ledger 或重复减少并发计数。

- [ ] **Step 2: 实现规范请求哈希**

实现 `HashRequest(tenantID, endpoint, key string, body []byte, limenHeaders map[string]string) (string, error)`：对 Header 名称排序、排除 Authorization、使用固定结构 JSON 和 SHA-256；缺少 tenant、endpoint 或 key 返回明确错误。

- [ ] **Step 3: 实现 MemoryStore**

实现 `NewMemoryStore()`、`CreateRun`、`AdmitRequest`、`BeginSettlement`、`SettleRequest`、`GetRequest` 和 `RecoverExpired`。所有方法用一把互斥锁保证测试中的强一致；`SettleRequest` 以 request ID 做幂等键，费用未知时保留 `SettlementPending`，不写成零。

- [ ] **Step 4: 运行并发测试并提交**

运行 `go test ./internal/run -race -count=1`，再提交 `git add internal/run && git commit -m "feat: add Run idempotency and memory store"`。

### Task 3：固定 PostgreSQL 表结构和 Store 接口

**Files:** 创建 `internal/store/store.go`、`internal/store/postgres.go`、`internal/store/migrations/001_run_ledger.sql`；测试 `internal/store/store_test.go`。

- [ ] **Step 1: 写迁移边界测试**

读取迁移 SQL，断言 `tenant_id` 出现在 tenants、runs、run_requests、attempts、ledger_entries 的主键/唯一键中；断言 ledger request 唯一、Run 状态约束和 RLS policy 存在。

- [ ] **Step 2: 定义 Store 接口**

让 `internal/store` 暴露租户显式参数的方法：`CreateRun(ctx, tenantID, run)`、`AdmitRequest(ctx, tenantID, input)`、`RecordAttemptStarted`、`SettleRequest`、`GetRun`、`GetRequest`；接口返回稳定的 `ErrIdempotencyConflict`、`ErrRequestInProgress`、`ErrRunNotActive` 和 `ErrAccountingSuspended`。

- [ ] **Step 3: 实现最小 PostgreSQL Repository**

使用 `database/sql` 事务和参数化 SQL；准入事务 `SELECT ... FOR UPDATE` 锁定 Run，先校验状态/截止时间/已结算成本/并发数，再插入 run_request 并递增 in-flight；结算事务锁定 request 和 Run，使用 `INSERT ... ON CONFLICT (tenant_id, request_id) DO NOTHING` 保证 Ledger 幂等。Repository 不保存 Prompt、Response 或明文 Key。

- [ ] **Step 4: 运行验证并提交**

运行 `go test ./internal/store -count=1`、`go vet ./...`，再提交 `git add internal/store && git commit -m "feat: add PostgreSQL Run store boundary"`。

### Task 4：同步文档和阶段验收

**Files:** 修改 `README.md`、`docs/design.md`、`AGENTS.md`。

- [ ] **Step 1: 更新文档**

明确 Run 目前只在领域层和 Store 边界可用，Chat 无 Run 行为不变；写清 soft budget 事后阈值、幂等冲突、结算 pending 和 PostgreSQL RLS/组合键约束。

- [ ] **Step 2: 全量检查**

运行 `go clean -testcache`、`make check`、`make build`、`git diff --check`。

- [ ] **Step 3: 提交文档**

提交 `git add README.md docs/design.md AGENTS.md docs/plans/2026-09-17-limen-phase-b-run-domain.md && git commit -m "docs: define Run and ledger boundaries"`。
