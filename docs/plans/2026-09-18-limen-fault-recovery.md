# Limen 进程崩溃与数据库断连验证计划

> 状态：已完成。

## 目标

使用真实进程和真实 PostgreSQL 验证两条恢复路径：执行实例在 Provider Attempt 开始后崩溃时转入诚实的未知费用状态；持久化结算任务遇到数据库短暂断连后只结算一次。

## 实现范围

1. 重新执行测试二进制作为独立工作进程，持久化 Request 租约和 `Attempt started` 后强制终止。
2. 租约过期后由另一 Store 恢复 Request；不重放 Provider，不写 Ledger，Run 进入 `suspended_accounting`。
3. 恢复事务把遗留的 `Attempt started` 标记为 `abandoned`，保留“调用可能已经发生”的审计语义。
4. 在结算任务已经持久化后暂停 PostgreSQL 容器，验证恢复调用明确失败且任务不丢失。
5. 数据库恢复后重新领取任务，Request 只结算一次、Ledger 只有一条、Run 释放并发名额。
6. 外部测试数据库无法由测试安全控制时，只跳过断连场景，其余真实 PostgreSQL 场景继续执行。

## 文件

- 修改 `internal/store/postgres_integration_test.go`：增加独立进程崩溃和数据库暂停/恢复场景。
- 修改 `internal/store/postgres.go`：在过期恢复事务中关闭遗留 Attempt。
- 修改 `scripts/postgres-integration.sh`：把临时容器名传入故障测试。
- 同步 `README.md`、`AGENTS.md`、`docs/design.md`、最终设计和变更记录。

## 验收

```bash
make integration
make check
make build
make smoke
git diff --check
```

测试不得访问真实 Provider，不得通过清空表或伪造零费用完成恢复。
