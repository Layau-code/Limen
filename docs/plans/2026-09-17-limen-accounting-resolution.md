# Limen 未知费用处置实施计划

## 目标

为 `suspended_accounting` 提供一个最小、可审计、幂等的恢复闭环，不重放可能已经产生费用的 Provider 请求。

## 实现范围

- Run 领域增加金额补记和接受未知费用两种互斥处置。
- 处置后 Request 进入 `settled`，`settlement_status` 为 `complete` 或 `unknown`；只有确定金额写入 Ledger。
- Run 按截止时间、软预算和完成标记恢复为 `active`、`completing` 或终态。
- 新增 `POST /v1/limen/runs/{run_id}/requests/{request_id}/accounting`，要求 `admin` Scope 和 `Idempotency-Key`。
- PostgreSQL 增加租户隔离的 `accounting_operations`，保存请求哈希、处置类型和金额摘要。
- 内存 Store 保留同等语义，供开发和 HTTP 测试使用。

## 明确不包含

- 自动查询 Provider 账单或重放上游请求。
- 费用预留、每日额度和硬预算。
- Prompt、Response、密钥或原始 Provider 错误持久化。

## 验收

- 补记金额最多生成一条 Ledger，重复幂等键返回同一 Request。
- 接受未知不产生 Ledger，且 Run 能恢复准入或按固定优先级进入终态。
- 非管理员、非法处置、跨 Run 请求和幂等冲突均返回稳定错误。
- `make check`、构建、冒烟和 `git diff --check` 全部通过。
