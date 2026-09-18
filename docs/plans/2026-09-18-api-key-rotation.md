# API Key 原子轮换实施计划

> **For agentic workers:** 按测试驱动方式逐项执行；每完成一个小步骤都运行对应测试并保持提交可回退。

**目标：** 为 PostgreSQL API Key 控制面增加租户内原子轮换，使旧 Key 立即失效、新 Key 只在首次响应返回明文，并支持幂等重试。

**架构：** 轮换在单个 PostgreSQL 事务中锁定旧 Key，创建新 Key、停用旧 Key 并记录幂等操作。HTTP 层只负责 Scope、请求校验和安全响应，认证层继续只接触摘要；重复请求只返回新 Key 元数据，不恢复明文。

**技术栈：** Go 标准库、database/sql、PostgreSQL 迁移、httptest 与现有集成测试脚本。

---

### Task 1：定义领域接口和 HTTP 契约

**文件：**
- 修改：`internal/auth/key_manager.go`
- 修改：`internal/httpapi/apikey_handlers.go`
- 修改：`internal/httpapi/handler.go`

- [x] 增加 `APIKeyManager.Rotate`，输入租户、旧公开前缀、新 Scope/有效期和幂等操作，输出旧 Key 元数据、新 Key 元数据和一次性明文。
- [x] 增加 `POST /v1/limen/keys/{public_prefix}/rotate`，只允许 `admin`，请求体严格限制为 `scopes` 和 `expires_at`，要求 `Idempotency-Key`。
- [x] 轮换响应只返回新 Key 的明文；重试响应保留新 Key 元数据但省略明文。

### Task 2：先写失败测试

**文件：**
- 修改：`internal/httpapi/apikey_handlers_test.go`
- 修改：`internal/store/apikey_manager_test.go`
- 修改：`internal/store/postgres_integration_test.go`

- [x] 测试 HTTP 轮换需要 admin、成功后返回新前缀和明文。
- [x] 测试相同幂等键重试不返回明文，修改请求体返回 `idempotency_conflict`。
- [x] 测试 PostgreSQL 轮换后旧 Key 认证失败、新 Key 认证成功，且数据库只保存摘要。
- [x] 测试跨租户旧前缀不能被轮换。
- [x] 先观察 HTTP 测试因路由不存在失败，再实现最小路径。

### Task 3：实现 PostgreSQL 原子轮换

**文件：**
- 修改：`internal/store/apikey_manager.go`
- 修改：`internal/store/migrations.go`（仅在确有新字段时修改）

- [x] 复用 `api_key_operations.public_prefix` 保存幂等操作产生的新前缀，不新增重复结果表。
- [x] 事务内锁定旧 Key，生成高熵新 Key，插入新摘要，停用旧 Key，再写入轮换操作记录。
- [x] 重复操作先校验请求哈希，再读取已生成的新 Key 元数据并省略明文。
- [x] 保持租户事务上下文、RLS 和参数化 SQL；任何失败都回滚全部状态。

### Task 4：补齐审计、文档和回归

**文件：**
- 修改：`internal/audit/audit.go`
- 修改：`internal/httpapi/audit.go`
- 修改：`README.md`
- 修改：`docs/design.md`
- 修改：`docs/specs/2026-09-17-limen-final-design.md`
- 修改：`AGENTS.md`
- 修改：`CHANGELOG.md`

- [x] 增加 `api_key.rotate` 审计动作，只记录前缀、请求哈希和结果，不记录明文。
- [x] 更新 API Key 生命周期、旧 Key 失效时序、一次性明文和 bootstrap 说明。
- [x] 增加生产方法简体中文用途注释和轮换测试要求。

### Task 5：验证与提交

- [x] 运行 `gofmt`、`go vet ./...`、`go test ./... -race`。
- [x] 运行 `make integration`、`make build`、`make smoke`、`git diff --check`。
- [ ] 提交：`feat: add atomic api key rotation`。
