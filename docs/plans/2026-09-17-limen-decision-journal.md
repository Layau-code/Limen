# Limen Decision Journal 阶段计划

## 目标

把能力路由从“只返回一次性计划”升级为可解释、可校验的决策记录，不保存 Prompt、Response 或 Provider 凭据。

## 实现

- `internal/journal` 定义按租户保存/读取 `DecisionInput` 和 `ExecutionPlan` 的最小接口。
- Memory Store 用于无数据库开发；PostgreSQL 使用 `decision_journal` JSONB 表、组合主键和 RLS。
- Dry Run 与真实 Chat 在 Provider 调用前生成 `decision_id` 并保存输入/计划。
- `GET /v1/limen/decisions/{decision_id}` 返回安全 Explain；Replay 仅调用无状态 Decision Engine，不访问 Provider 或熔断器。
- Replay 返回 `match` 和最小差异码，算法版本不可用时返回稳定错误。

## 明确不包含

- 配置发布、算法版本注册表、旧算法迁移和完整结构化差异树。
- Prompt/Response 持久化、Provider 调用重放和语义级结果比较。

## 验收

```bash
make check
make build
make smoke
```

测试覆盖租户隔离、哈希校验、Explain、Replay 和不访问 Provider。
