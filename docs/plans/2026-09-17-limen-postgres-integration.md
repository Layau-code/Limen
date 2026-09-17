# Limen PostgreSQL 多实例验证计划

> 状态：已完成。

## 目标

用真实 PostgreSQL 验证 Run 治理的关键不变量，补齐仅靠内存测试和 SQL 文本检查无法证明的并发、租户隔离与故障恢复边界。

## 实现范围

1. 新增可重复执行的 PostgreSQL 集成测试入口；本地默认使用临时 Docker 容器，也允许复用外部测试数据库。
2. 使用非超级用户连接验证 RLS，证明租户会话不能读取或写入其他租户数据。
3. 对同一 Run 并发发起 100 次准入，验证 `max_parallelism=8` 时只有 8 个请求占用名额。
4. 对同一幂等键并发发起 100 次准入，验证只产生一个 Request，且请求体冲突稳定返回幂等冲突。
5. 并发结算同一 Request，验证 Ledger 最多一条、Run 成本只累计一次。
6. 由两个 Store 实例竞争恢复同一过期租约，验证 Request 只恢复一次、Run 进入 `suspended_accounting` 且不再占用并发名额。
7. 两个数据库连接验证取消事件既可通过 `LISTEN/NOTIFY` 传播，也可通过事件表轮询恢复。
8. 将集成测试加入 Makefile 和 CI；普通 `make check` 不依赖 Docker。

## 文件

- 新增 `internal/store/postgres_integration_test.go`：真实数据库场景与测试夹具。
- 新增 `scripts/postgres-integration.sh`：临时数据库生命周期和受限角色初始化。
- 修改 `Makefile`：增加 `integration` 命令。
- 修改 `.github/workflows/ci.yml`：执行 PostgreSQL 集成测试。
- 修改 `README.md`、`AGENTS.md`、`docs/design.md`：记录验证边界和运行方式。

## 验收

```bash
make integration
make check
make build
git diff --check
```

集成测试必须使用真实 PostgreSQL，不使用 SQL Mock；测试数据库只保存随机测试标识，不包含 Prompt、Response 或密钥。
