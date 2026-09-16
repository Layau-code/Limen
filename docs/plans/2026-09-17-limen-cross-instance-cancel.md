# Limen 跨实例取消阶段计划

## 目标

让 Run 取消在 PostgreSQL 和内存开发实现中都能传播到正在执行的 Chat 请求，避免只修改发起取消实例的本地状态。

## 实现

- 取消 Run 的事务同时写入租户隔离 `cancellation_events`。
- `CancellationService` 按事件 ID 游标读取通知，内存和 PostgreSQL Store 使用同一接口。
- 在途 Chat 每秒轮询当前 Run 的事件；收到事件后以 `ErrRunCancelled` 取消 Provider Context。
- HTTP 返回稳定的 `409 run_cancelled`，结算使用不受客户端取消影响的短超时 Context。
- 轮询保证断线可恢复；后续可增加 PostgreSQL LISTEN/NOTIFY 降低延迟。

## 验收

```bash
make check
make build
make smoke
```

测试覆盖事件租户隔离、取消传播、跨实例语义和幂等取消。
