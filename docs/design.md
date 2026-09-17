# Limen 可靠性网关设计

## 目标

Limen 面向 Agent 提供统一的 OpenAI 兼容入口。客户端使用稳定的逻辑模型名，Limen 在启动时或配置版本发布时加载只读注册表，选择 Provider 和真实模型，并在明确的瞬时故障下安全切换备用目标。

## 数据流与边界

```text
HTTP Principal/Scope 鉴权与解析
  → ModelRegistry 提供当前配置版本的只读能力目录
  → Decision Engine 生成带哈希的 ExecutionPlan
  → Decision Journal 保存输入与计划
  → Router Executor 创建总预算并检查 Circuit Breaker
  → Provider 执行一次协议转换和上游调用
  → 路由摘要响应头
  → 普通响应或 SSE 转发
  → 响应结束后汇总成本并写入 Trailer / 结构化日志
```

- `internal/httpapi`：鉴权、请求校验、错误映射、响应转发和安全日志。
- `internal/auth`：常量时间校验静态 Bearer Key，生成不携带原始 Key 的租户 Principal，并集中定义 Scope；`internal/store` 提供 PostgreSQL HMAC Key Store 实现。
- `internal/config`：严格解析环境变量和模型 JSON，只在启动时校验密钥与路由参数。
- `internal/configstore`：保存不可变配置版本，内存实现用于开发，PostgreSQL 实现用于多实例恢复。
- `internal/credentialstore`：使用 AES-GCM 加密 Provider 凭据，并将密文绑定到租户、Provider 和 endpoint。
- `internal/catalog`：保存逻辑模型、目标能力和数据等级；兼容模式匹配 `gpt-*`、`o1-*`、`o3-*`、`claude-*`。
- `internal/decision`：只消费版本化快照，按硬约束过滤候选并稳定排序，输出 `InputHash`、`PlanHash` 和原因码；算法注册表负责 Replay 的版本解析，未知版本不回退。
- `internal/journal`：按租户保存不含正文的 DecisionInput/ExecutionPlan；PostgreSQL 实现使用 JSONB 和组合主键，内存实现只用于无数据库开发。
- `internal/gateway/registry.go`：保留旧导出名的兼容包装，不再承载目录实现。
- `internal/gateway/router.go`：将请求快照交给 Decision Engine，替换上游模型，管理共享总预算、单次超时、Fallback 和计划执行。
- `internal/gateway/router.go`：配置发布通过带读写锁的目录快照原子切换；配置版本和路由策略进入后续决策输入。
- `internal/gateway/breaker.go`：按逻辑模型目标隔离的进程内并发安全熔断器。
- `internal/provider`：OpenAI 与 Anthropic 的鉴权、请求转换、响应转换和 SSE 转换；不感知逻辑模型。
- `internal/cost`：解析每百万 Token 的十进制定价，使用定点整数计算成本；不负责路由或存储。

## 模型与路由

模型文件格式如下，目标顺序就是优先级：

```json
{
  "routing": {"attempt_timeout":"10s", "failure_threshold":3, "cooldown":"30s"},
  "models": [{
    "id":"smart-model",
    "targets":[
      {
        "provider":"openai",
        "upstream_model":"gpt-5-mini",
        "pricing":{"input_per_million_usd":"0.250000", "output_per_million_usd":"2.000000"}
      },
      {
        "provider":"anthropic",
        "upstream_model":"claude-sonnet-4-20250514",
        "pricing":{"input_per_million_usd":"3.000000", "output_per_million_usd":"15.000000"}
      }
    ]
  }]
}
```

`id`、`targets`、目标的 `provider` 和 `upstream_model` 必填；Provider 只能是 `openai` 或 `anthropic`；ID 和目标组合不能重复；文件使用严格未知字段校验。总请求预算由 `LIMEN_REQUEST_TIMEOUT` 控制，单次超时和熔断参数由文件中的 `routing` 控制。

目标可声明 `id`、`capabilities`（当前仅 `text`）、`supports_streaming`、`quality_tier`、`cost_tier`、`context_window` 和 `data_classes`（`public`、`internal`、`confidential`、`restricted`）。旧配置缺少这些字段时补全基础文本默认值；缺少目标 ID 时按 `provider:upstream_model` 派生稳定 ID。

目标可以增加可选的 `pricing` 对象，包含 `input_per_million_usd` 和 `output_per_million_usd` 两个十进制字符串。Limen 在响应完成后汇总 Provider 报告的用量；SSE 继续实时转发，未知费用不写成零。完整边界见 [`docs/specs/2026-09-16-usage-cost-settlement-design.md`](specs/2026-09-16-usage-cost-settlement-design.md)。

没有模型文件时使用兼容注册表，模型名原样透传，不执行跨 Provider Fallback。配置模式的 `/v1/models` 使用 `owned_by=limen`，兼容模式使用实际 Provider。

请求 `model=auto` 或携带 `limen` 契约时，Decision Engine 依次执行启用、安全、能力、质量下限、流式、上下文、数据等级、健康和最小尝试窗口过滤。`minimum_quality_tier` 是硬约束，低于门槛的目标返回 `quality_tier_too_low`，不会进入 Fallback 计划。`balanced` 优先健康和质量，`economy` 优先预计定点成本；治理 Run 接近软预算时只切换后续请求策略，不对单个请求预留或硬拦截费用。计划使用规范 JSON 和 SHA-256 哈希，便于审计和后续 Replay；Replay 只重演决策计划，不重放 Provider 请求。

`Router` 是计划执行器而不是策略实现者：它在执行前再次原子获取熔断探测权，若 Half-Open 被并发请求占用则记录 `skipped_due_to_race` 并继续下一个计划目标。Provider 负责协议转换和错误分类（`retryable_transient`、`deterministic_request`、`authentication`、`quota`、`internal`），Router 只根据归一化分类决定是否 Fallback，不读取能力契约或模型映射。

阶段 A 已提供 `POST /v1/limen/decisions/dry-run`：它复用同一解析和决策路径，只返回不含正文的计划，不访问 Provider、不改变熔断和结算状态。模型文件经规范化 JSON 计算 `config_version`，供后续 Run 固定配置版本。

当前 Decision Journal 在真实 Chat 调用 Provider 前写入决策快照，并通过 `X-Limen-Decision-ID` 暴露不含正文的标识。`GET /v1/limen/decisions/{decision_id}` 返回输入与计划，`POST /v1/limen/decisions/{decision_id}/replay` 只使用历史输入调用无状态 Decision Engine，对比 `plan_hash`，不访问 Provider 或当前熔断器。

阶段 B 已建立 `internal/run` 领域状态机和 `internal/store` 持久化边界。Run 的 `Admit` 只检查 active、截止时间、已结算软预算和在途并发数；`Settle` 才累计费用，未知费用进入 `suspended_accounting`。同一租户、接口和 Idempotency-Key 使用规范请求哈希去重，PostgreSQL 迁移通过租户组合键、RLS 和唯一账本约束阻止跨租户访问。无 Run 的兼容 Chat 路径不读取该状态；显式启用内存控制面后，受治理 Chat 才会执行 Run 准入和请求结算。

当前 HTTP Run 和配置控制面通过 `LIMEN_DATABASE_URL` 启用 PostgreSQL 持久化；启动会 Ping 数据库并执行版本化迁移，008 迁移对所有租户表启用 `FORCE ROW LEVEL SECURITY`。未配置数据库时，控制面使用内存实现，仅适合单机开发，不能作为生产账本或配置发布记录。`LIMEN_TENANT_ID` 绑定当前静态 Key 的开发租户，`LIMEN_API_SCOPES` 控制该 Key 可用接口；启用 PostgreSQL Key Store 后，租户和 Scope 从数据库 Key 记录生成。凭据和取消的 `NOTIFY` 都只是低延迟提示，通知失败不回滚已提交事务，轮询或重启负责兜底。

受治理 Chat 在 Request 准入时写入执行实例租约，默认 30 秒过期、每 10 秒续租，响应结束后释放。每次真实 Provider 调用前单独写入 Attempt，收到上游非敏感 request ID 后补写，Fallback 后续目标不会覆盖前一个 Attempt 的状态。结算遇到暂时性存储错误时，当前进程按 0、100、500 毫秒退避重试；仍未完成则写入持久化 `settlement_jobs` 并返回 `pending`。后台任务使用独立租约幂等重试已知费用；未知费用只转为 `suspended_accounting`，不重放 Provider。主进程同时扫描当前租户的过期请求；恢复任务将未知费用请求标记为 `abandoned/pending`，暂停关联 Run 的账本。取消 Run 时在同一事务写入租户隔离取消事件，PostgreSQL 实例优先通过 `LISTEN/NOTIFY` 广播，在途 Chat 同时每秒轮询事件作为断线兜底。这样既避免实例崩溃永久占用并发名额，也不把可能已经发生的上游费用伪造成零。

未知费用的恢复由管理员显式完成：`POST /v1/limen/runs/{run_id}/requests/{request_id}/accounting` 使用 `mode=cost` 补记定点金额，或使用 `mode=accept_unknown` 接受无法核实的费用。两种模式都把 Request 置为 `settled`，分别标记 `settlement_status=complete/unknown`；只有补记金额才写入 Ledger。Run 按取消、截止时间、软预算、完成标记的固定优先级恢复为终态、`active` 或 `completing`，已进入终态的 Run 不会被重新打开；同一 Run 的多个未知请求必须全部处置后才恢复准入，并使用控制面幂等键避免重复处置。

开发控制面已覆盖 Run 创建、查询、完成、取消、Request 结算查询、未知费用处置和配置版本发布；控制变更使用 `Idempotency-Key` 与规范请求哈希。配置版本由规范 JSON 的 SHA-256 生成，发布只改变当前快照，旧版本保留为 `superseded`。受治理 Chat 在准入后记录本地 Attempt、响应结束后进入结算，已知成本写入唯一账本，未知成本返回 `pending` 并暂停 Run。管理员可对暂停请求补记确定金额，或明确接受未知费用；处置事务锁定 Request 和 Run，重复幂等键不会重复记账。暂停期间可以先请求完成，Run 会保留 `complete_requested`，不会跳过对账直接完成。

## 可靠性不变量

1. Router 为一次调用创建一个总 Context；每个目标的 Context 只能更早截止，切换不会重新获得预算。
2. 每个目标最多发起一次调用；瞬时状态固定为 408、409、429、500、502、503、504、529，传输错误同样允许切换。
3. 确定性状态、请求转换错误、客户端取消和总预算耗尽不触发下一个目标。
4. 目标返回 `2xx` 后立即交给客户端；即使后续 SSE 读取失败，也不重放请求。
5. 瞬时失败达到阈值后目标进入 Open；熔断键包含逻辑模型、Provider 和上游模型，冷却后只放行一个 Half-Open 探测，成功或确定性响应关闭，瞬时失败重新计时。
6. 被放弃的响应体立即关闭；最终响应关闭时释放上游连接和关联 Context。

## 错误与可解释性

Provider 将本地构造错误标记为 `RequestError`，网络和 Context 错误标记为 `TransportError`；上游响应同时提供 `retryable_transient`、`deterministic_request`、`authentication`、`quota` 或 `internal` 分类。Router 使用 `UnsupportedModelError`、`NoAvailableTargetError` 和 `RouteError`，HTTP 层统一映射为 OpenAI 风格错误；已有的最终上游状态和正文继续透传。

Chat API 当前支持 `model`、文本 `messages`、`max_tokens`、`temperature`、`stream` 和 Limen 能力契约。Tools、tool calls、`response_format`、`n`、`logprobs`、多模态内容以及未知字段均显式返回 `400`；这组边界在引入 Responses、Tools 或 Vision 前保持稳定。

响应头包含安全路由摘要：`X-Limen-Provider`、`X-Limen-Attempts`、`X-Limen-Route`；响应结束后通过 Trailer 增加结算状态、Token 和可用成本。日志读取这些字段，不记录 API Key、上游模型、Prompt 或完整 Response。路径长度受每个模型最多四个目标限制。

## 健康与交付

`/livez` 只表示进程可响应；`/readyz` 表示启动依赖已完成，关闭时先变为未就绪再执行 `Server.Shutdown`。`limen version` 和 `limen healthcheck` 不读取业务密钥；Docker 使用静态非 root 运行时。完整运维说明见 [`docs/operations.md`](operations.md)。

## 出站安全

生产 Provider Client 使用 HTTPS allowlist，禁用环境代理和自动重定向，解析目标地址时拒绝 loopback、私网、链路本地、组播、未指定和云元数据地址。Provider Key 只绑定到对应适配器，不进入决策输入、路由头或日志。测试通过注入 `httptest` Client 和解析器覆盖这些边界。

## 明确不包含

本版本不实现每日额度和超额拦截、模型文件热加载、远程配置、同目标重试、动态权重、随机负载均衡、成本路由、语义缓存、Prompt 分类、分布式熔断、Secret Manager 接入、大型管理后台或完整 OpenTelemetry 导出平台；PostgreSQL API Key Store、配置版本存储、Provider 凭据轮换/撤销 API 和基础 Prometheus 文本指标已实现。
