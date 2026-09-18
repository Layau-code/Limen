# Limen 管理审计与 Replay 保留语义

## 目标

在不记录 Prompt、Response、API Key 或真实上游模型名的前提下，提供可查询的控制面变更证据，并让历史 Decision Replay 在算法版本退役后明确失败，不静默回退。

## 实现

- 新增租户隔离的 `audit.Store`，提供内存和 PostgreSQL 实现。
- 控制面记录配置创建/发布、凭据轮换/撤销、Run 完成/取消和未知费用处置。
- 新增受 `admin` Scope 保护的 `GET /v1/limen/audit`，限制查询数量为 1 到 100。
- 事件 ID 由租户、动作、资源和请求哈希稳定生成，重复控制重试不会制造重复事件。
- 新增 `AlgorithmRegistry.RegisterWithRetention` 与 `ResolveAt`；保留截止时间到达后返回 `algorithm_version_unavailable`。

## 边界

审计追加故障不回滚已经提交的业务变更，因此它是安全操作证据而非事务审批流。算法保留窗口由注册时的 `retainUntil` 明确表达，当前内置版本不自动回退到其他版本；需要退役旧版本时必须保留旧 Engine 到截止时间之后再移除。

## 验证

- 内存审计租户隔离、字段校验和稳定去重测试。
- PostgreSQL 迁移包含组合主键、RLS 和 `FORCE ROW LEVEL SECURITY`。
- HTTP 审计接口覆盖 admin 鉴权、数量上限和真实上游模型名不泄露。
- 算法注册表覆盖截止时间前可 Replay、到期拒绝和重复注册拒绝。
