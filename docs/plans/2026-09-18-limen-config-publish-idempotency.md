# Limen 配置发布幂等计划

## 目标

避免客户端在发布请求超时后重试造成重复切换或跨版本误操作，使配置发布具备和 Run 控制面一致的幂等语义。

## 实施

1. 为配置 Store 增加 `PublishWithMutation` 接口，记录租户、固定操作名、幂等键、请求哈希和版本。
2. 内存 Store 与 PostgreSQL 使用相同的冲突规则；PostgreSQL 通过 `config_operations` 表和 RLS 保证跨实例一致。
3. HTTP 发布接口强制 `Idempotency-Key`，哈希绑定租户、路径和目标版本；相同键同哈希返回首次结果，不同版本返回 `idempotency_conflict`。
4. 发布状态切换和幂等记录放在同一个事务中，通知仍只发送租户和版本哈希。

## 验收

- 同键同版本重试不新增状态切换记录。
- 同键不同版本返回 `409 idempotency_conflict`。
- 内存、HTTP 和 PostgreSQL 迁移测试覆盖租户隔离与 RLS。
