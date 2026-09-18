# Limen Decision Journal 完整性计划

## 目标

确保 Explain/Replay 使用的历史决策证据没有被数据库 JSONB 内容或摘要列静默篡改。

## 规则

- 保存和读取都重新计算 `input_hash` 与 `plan_hash`。
- PostgreSQL 读取同时比较 `input_hash`、`plan_hash` 列和 JSONB 反序列化后的计算结果。
- 任一摘要不一致都返回错误，不返回部分可信的历史计划。
- 内存 Store 与 PostgreSQL Store 使用同一记录校验语义。

## 验收

- 内存记录被篡改后读取失败。
- PostgreSQL `plan_json` 被修改而摘要列不变时读取失败。
- 正常带价格 Decision Journal round-trip 和 Replay 继续通过。
