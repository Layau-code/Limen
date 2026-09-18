# Limen 配置发布双人审批设计

## 1. 目标与范围

为配置发布增加可选的双人审批能力，强化 Limen 的治理和审计差异化，同时保持单机开发体验不变。

本阶段只治理 `config.publish`，不改变 Provider 调用、Run 结算、API Key 生命周期和凭据轮换语义。审批功能默认关闭，生产部署通过 `LIMEN_CONFIG_APPROVAL_REQUIRED=true` 开启。

开启后，配置发布必须满足：

1. 发起者创建一个绑定配置版本和发布幂等键的审批请求；
2. 不同 `actor_id` 的管理员批准；
3. 发起者使用同一个发布幂等键提交配置发布。

静态 Key 模式的 `actor_id` 固定为 `static`，无法满足双人身份分离；开启审批的生产环境必须使用 PostgreSQL API Key Store。

## 2. 身份与安全边界

`actor_id` 只使用非敏感身份：静态模式为固定 `static`，PostgreSQL 模式为 API Key 公开前缀。原始 API Key、Authorization、配置正文和真实上游模型名不得进入审批记录、审计、日志或响应。

审批记录始终绑定 `tenant_id`。所有读取、写入、批准和发布校验都必须在租户事务中完成，并受 PostgreSQL RLS 保护。客户端不能提交租户 ID，租户只来自 Principal。

## 3. 数据模型

审批记录保存以下字段：

| 字段 | 语义 |
| --- | --- |
| `tenant_id` | 租户边界 |
| `approval_id` | 不可猜测的资源 ID |
| `config_version` | 目标配置版本哈希 |
| `publish_idempotency_key` | 对应发布操作使用的幂等键 |
| `request_hash` | 版本、路径和发布幂等键的规范哈希 |
| `requested_by` | 发起者 `actor_id` |
| `approved_by` | 批准者 `actor_id`，未批准时为空 |
| `state` | `pending`、`approved`、`consumed`、`rejected` 或 `expired` |
| `expires_at` | 审批有效期，默认创建后 30 分钟 |
| `created_at`、`updated_at` | 审计时间 |

唯一约束为 `(tenant_id, approval_id)`；创建、批准和拒绝操作分别使用租户、endpoint、Idempotency-Key 和请求哈希实现幂等。

## 4. 状态与接口

```text
pending ──approve──→ approved ──publish success──→ consumed
   └────reject─────→ rejected
   └────expiry─────→ expired
```

新增控制面接口：

```text
POST /v1/limen/configs/{version}/approvals
GET  /v1/limen/configs/{version}/approvals/{approval_id}
POST /v1/limen/configs/{version}/approvals/{approval_id}/approve
POST /v1/limen/configs/{version}/approvals/{approval_id}/reject
```

所有接口需要 `admin` Scope，并要求 `Idempotency-Key`（查询接口除外）。创建请求体为：

```json
{"publish_idempotency_key":"publish-config-001"}
```

创建时服务端读取配置版本并计算 `request_hash`，不保存配置正文。批准和拒绝请求体必须为空对象 `{}`。

当 `LIMEN_CONFIG_APPROVAL_REQUIRED=false` 时，配置发布保持当前行为，审批接口仍可用于演示和审计，但不会成为发布前置条件。

当开关为 `true` 时，发布请求必须携带：

```text
X-Limen-Approval-ID: approval_...
Idempotency-Key: publish-config-001
```

发布服务端校验：审批属于当前租户和配置版本、发布幂等键与哈希一致、状态为 `approved`、未过期、`requested_by` 与当前发布者一致。配置发布幂等操作成功后，审批转为 `consumed`；如果 Router 激活失败，同一发布幂等键的重试仍可使用已 `consumed` 的同一审批重新应用 Router 快照，但不能用于其他配置或发布键。

## 5. 错误语义

- 缺少审批头：`409 approval_required`
- 审批不存在或跨租户：`404 approval_not_found`
- 审批已过期：`409 approval_expired`
- 审批状态不允许当前操作：`409 approval_state_conflict`
- 发布键或配置版本不一致：`409 approval_binding_conflict`
- 发起者批准自己：`409 approval_actor_not_distinct`
- 非管理员或租户不匹配：沿用 `403 insufficient_scope`，不泄露跨租户资源存在性
- 相同幂等键不同请求哈希：`409 idempotency_conflict`

审批失败不能改变配置版本和 Router 当前快照；发布失败不能消耗审批。

## 6. 审计与可观测性

审计动作：

- `config.approval.requested`
- `config.approval.approved`
- `config.approval.rejected`
- `config.approval.consumed`

事件记录 actor、配置版本摘要、审批 ID、结果和请求哈希，不记录配置正文、Provider Key 或 API Key。Metrics 只记录固定的审批结果和错误码，不使用审批 ID、租户 ID 或 actor 作为标签。

## 7. 实现边界

- 内存 Store 用于单元测试和本地演示；启用 PostgreSQL 时审批状态以数据库为唯一事实来源。
- PostgreSQL 迁移新增审批表、操作幂等表、组合外键、RLS 和 `FORCE ROW LEVEL SECURITY`。
- 审批校验、配置发布幂等记录和审批消费必须在同一个 PostgreSQL 事务中完成；Router 快照替换仍在事务提交后执行。快照替换失败时，重复的同键发布请求只执行快照修复，不重复发布或产生新的审批消费。
- 不引入通用工作流引擎、消息队列、定时任务平台或前端后台。

## 8. 验收标准

- 默认关闭时，现有配置发布测试和客户端行为完全不变。
- 开启后，未审批、错误版本、错误发布键、过期审批和自己批准自己均被拒绝。
- 两个不同 PostgreSQL API Key 的管理员可以完成一次发布；审批只能消费一次。
- 同一审批/批准/发布幂等键并发请求最多产生一个状态转换；不同请求哈希稳定返回冲突。
- 跨租户猜测审批 ID、配置版本或发布键不泄露资源存在性。
- 真实 PostgreSQL 测试覆盖 RLS、并发批准、过期恢复、Router 发布失败重试和审计 actor。
- `make check`、`make integration`、`make build`、`make smoke` 和 `git diff --check` 全部通过。
