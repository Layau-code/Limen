# Limen 可靠性网关设计

## 目标

Limen 面向 Agent 提供统一的 OpenAI 兼容入口。客户端使用稳定的逻辑模型名，Limen 在启动时加载只读注册表，选择 Provider 和真实模型，并在明确的瞬时故障下安全切换备用目标。

## 数据流与边界

```text
HTTP 鉴权与解析
  → ModelRegistry 提供只读能力目录
  → Decision Engine 生成带哈希的 ExecutionPlan
  → Router Executor 创建总预算并检查 Circuit Breaker
  → Provider 执行一次协议转换和上游调用
  → 路由摘要响应头
  → 普通响应或 SSE 转发
  → 响应结束后汇总成本并写入 Trailer / 结构化日志
```

- `internal/httpapi`：鉴权、请求校验、错误映射、响应转发和安全日志。
- `internal/config`：严格解析环境变量和模型 JSON，只在启动时校验密钥与路由参数。
- `internal/catalog`：保存逻辑模型、目标能力和数据等级；兼容模式匹配 `gpt-*`、`o1-*`、`o3-*`、`claude-*`。
- `internal/decision`：只消费版本化快照，按硬约束过滤候选并稳定排序，输出 `InputHash`、`PlanHash` 和原因码。
- `internal/gateway/registry.go`：保留旧导出名的兼容包装，不再承载目录实现。
- `internal/gateway/router.go`：将请求快照交给 Decision Engine，替换上游模型，管理共享总预算、单次超时、Fallback 和计划执行。
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

请求 `model=auto` 或携带 `limen` 契约时，Decision Engine 依次执行启用、安全、能力、流式、上下文、数据等级、健康和最小尝试窗口过滤。`balanced` 优先健康和质量，`economy` 优先预计定点成本；治理 Run 接近软预算时只切换后续请求策略，不对单个请求预留或硬拦截费用。计划使用规范 JSON 和 SHA-256 哈希，便于审计和后续 Replay；Replay 只重演决策计划，不重放 Provider 请求。

`Router` 是计划执行器而不是策略实现者：它在执行前再次原子获取熔断探测权，若 Half-Open 被并发请求占用则记录 `skipped_due_to_race` 并继续下一个计划目标。Provider 只负责协议转换，不读取能力契约或模型映射。

阶段 A 已提供 `POST /v1/limen/decisions/dry-run`：它复用同一解析和决策路径，只返回不含正文的计划，不访问 Provider、不改变熔断和结算状态。模型文件经规范化 JSON 计算 `config_version`，供后续 Run 固定配置版本。

## 可靠性不变量

1. Router 为一次调用创建一个总 Context；每个目标的 Context 只能更早截止，切换不会重新获得预算。
2. 每个目标最多发起一次调用；瞬时状态固定为 408、409、429、500、502、503、504、529，传输错误同样允许切换。
3. 确定性状态、请求转换错误、客户端取消和总预算耗尽不触发下一个目标。
4. 目标返回 `2xx` 后立即交给客户端；即使后续 SSE 读取失败，也不重放请求。
5. 瞬时失败达到阈值后目标进入 Open；熔断键包含逻辑模型、Provider 和上游模型，冷却后只放行一个 Half-Open 探测，成功或确定性响应关闭，瞬时失败重新计时。
6. 被放弃的响应体立即关闭；最终响应关闭时释放上游连接和关联 Context。

## 错误与可解释性

Provider 将本地构造错误标记为 `RequestError`，网络和 Context 错误标记为 `TransportError`。Router 使用 `UnsupportedModelError`、`NoAvailableTargetError` 和 `RouteError`，HTTP 层统一映射为 OpenAI 风格错误；已有的最终上游状态和正文继续透传。

Chat API 当前支持 `model`、文本 `messages`、`max_tokens`、`temperature`、`stream` 和 Limen 能力契约。Tools、tool calls、`response_format`、`n`、`logprobs`、多模态内容以及未知字段均显式返回 `400`；这组边界在引入 Responses、Tools 或 Vision 前保持稳定。

响应头包含安全路由摘要：`X-Limen-Provider`、`X-Limen-Attempts`、`X-Limen-Route`；响应结束后通过 Trailer 增加结算状态、Token 和可用成本。日志读取这些字段，不记录 API Key、上游模型、Prompt 或完整 Response。路径长度受每个模型最多四个目标限制。

## 健康与交付

`/livez` 只表示进程可响应；`/readyz` 表示启动依赖已完成，关闭时先变为未就绪再执行 `Server.Shutdown`。`limen version` 和 `limen healthcheck` 不读取业务密钥；Docker 使用静态非 root 运行时。完整运维说明见 [`docs/operations.md`](operations.md)。

## 出站安全

生产 Provider Client 使用 HTTPS allowlist，禁用环境代理和自动重定向，解析目标地址时拒绝 loopback、私网、链路本地、组播、未指定和云元数据地址。Provider Key 只绑定到对应适配器，不进入决策输入、路由头或日志。测试通过注入 `httptest` Client 和解析器覆盖这些边界。

## 明确不包含

本版本不实现每日额度和超额拦截、热加载、远程配置、同目标重试、动态权重、随机负载均衡、成本路由、语义缓存、Prompt 分类、分布式熔断、数据库、管理后台或完整 Prometheus/OpenTelemetry 平台。
