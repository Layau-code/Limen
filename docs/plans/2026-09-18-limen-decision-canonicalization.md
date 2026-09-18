# Limen 决策规范化与算法版本计划

## 目标

修复语义相同但集合字段顺序不同导致的决策哈希差异，同时不改变已经持久化的 `decision.v1` 计划。

## 实施

1. 保留 `decision.v1` 的原有序列化和计划哈希语义。
2. 新增 `decision.v2`，对能力、数据等级等无序集合排序去重。
3. `model=auto` 的候选按 `model_id` 和 `target_id` 稳定排序；显式模型的目标顺序保持配置优先级，避免改变 Fallback 行为。
4. Router 新生成的 DecisionInput 使用 V2；算法注册表同时保留 V1 和 V2，未知版本不回退。
5. 增加语义等价输入的哈希和计划字节测试，并保留 100 组 V1 golden fixture 回归。

## 验收

- 同一 V2 语义输入无论集合顺序如何，`input_hash`、计划字节和 `plan_hash` 均一致。
- V1 golden fixture 全部保持原哈希。
- 显式模型的配置目标优先级和已有 Fallback 测试不改变。
- `go test ./internal/decision ./internal/gateway`、`make check` 和构建通过。
