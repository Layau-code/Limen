# Limen 请求租约与崩溃恢复计划

## 目标

让受治理 Request 在多实例执行时有明确的持有者、续租和恢复边界，避免进程崩溃后永久占用 Run 并发名额，也避免恢复任务盲目重放可能已经产生费用的 Provider 调用。

## 实现

- `AdmissionInput` 在准入事务内写入 `lease_owner` 和 `lease_expires_at`。
- `LeaseService` 提供获取、续租、释放和按租户扫描过期请求的方法。
- HTTP 请求默认持有 30 秒租约，每 10 秒续租，响应结束释放；续租失败取消本地 Context。
- 主进程按 10 秒间隔扫描过期租约，将在途请求标记为 `abandoned/pending`，并把 Run 置为 `suspended_accounting`。
- Memory Store 与 PostgreSQL Store 使用相同的租约语义；PostgreSQL 恢复使用行锁和 `SKIP LOCKED`。

## 安全边界

恢复过程不假设上游调用没有发生，不重放 Provider，不把未知费用写成零；后续对账能力完成前，Run 不再接受新准入。

## 验收

```bash
make check
make build
make smoke
```

测试覆盖租约抢占、续租、释放、过期恢复、租户隔离和竞态执行。
