# Limen Replay 结构化差异计划

## 目标

让 Replay 能解释计划为何变化，而不是只返回 `plan_hash_mismatch`，同时保证差异响应不泄露上游模型名或业务正文。

## 实施

1. 比较算法/配置版本、策略、目标逻辑 ID 顺序、候选原因/接受状态和哈希。
2. 用固定路径和 `changed/added/removed` 类型输出，保证客户端可消费。
3. 只读取 `model_id`、`target_id`、原因码和哈希等安全字段，不序列化完整 `catalog.Target`。
4. 添加差异顺序稳定和上游模型哨兵不泄露测试。

## 验收

- 计划变化能返回具体结构化路径。
- 相同计划仍返回空差异和 `match=true`。
- Replay 响应不包含上游模型名、Prompt 或 Response。
