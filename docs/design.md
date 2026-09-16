# Limen 可靠性网关设计

## 目标

Limen 面向 Agent 提供统一的 OpenAI 兼容入口。客户端使用稳定的逻辑模型名，Limen 在启动时加载只读注册表，选择 Provider 和真实模型，并在明确的瞬时故障下安全切换备用目标。

## 数据流与边界

```text
HTTP 鉴权与解析
  → ModelRegistry 解析逻辑模型
  → Router 创建总预算并检查 Circuit Breaker
  → Provider 执行一次协议转换和上游调用
  → 路由摘要响应头
  → 普通响应或 SSE 转发
```

- `internal/httpapi`：鉴权、请求校验、错误映射、响应转发和安全日志。
- `internal/config`：严格解析环境变量和模型 JSON，只在启动时校验密钥与路由参数。
- `internal/gateway/registry.go`：保存逻辑模型及一至四个有序目标；兼容模式匹配 `gpt-*`、`o1-*`、`o3-*`、`claude-*`。
- `internal/gateway/router.go`：替换上游模型，管理共享总预算、单次超时、Fallback 和路由决策。
- `internal/gateway/breaker.go`：按逻辑模型目标隔离的进程内并发安全熔断器。
- `internal/provider`：OpenAI 与 Anthropic 的鉴权、请求转换、响应转换和 SSE 转换；不感知逻辑模型。

## 模型与路由

模型文件格式如下，目标顺序就是优先级：

```json
{
  "routing": {"attempt_timeout":"10s", "failure_threshold":3, "cooldown":"30s"},
  "models": [{
    "id":"smart-model",
    "targets":[
      {"provider":"openai", "upstream_model":"gpt-5-mini"},
      {"provider":"anthropic", "upstream_model":"claude-sonnet-4-20250514"}
    ]
  }]
}
```

`id`、`targets`、目标的 `provider` 和 `upstream_model` 必填；Provider 只能是 `openai` 或 `anthropic`；ID 和目标组合不能重复；文件使用严格未知字段校验。总请求预算由 `LIMEN_REQUEST_TIMEOUT` 控制，单次超时和熔断参数由文件中的 `routing` 控制。

没有模型文件时使用兼容注册表，模型名原样透传，不执行跨 Provider Fallback。配置模式的 `/v1/models` 使用 `owned_by=limen`，兼容模式使用实际 Provider。

## 可靠性不变量

1. Router 为一次调用创建一个总 Context；每个目标的 Context 只能更早截止，切换不会重新获得预算。
2. 每个目标最多发起一次调用；瞬时状态固定为 408、409、429、500、502、503、504、529，传输错误同样允许切换。
3. 确定性状态、请求转换错误、客户端取消和总预算耗尽不触发下一个目标。
4. 目标返回 `2xx` 后立即交给客户端；即使后续 SSE 读取失败，也不重放请求。
5. 瞬时失败达到阈值后目标进入 Open；熔断键包含逻辑模型、Provider 和上游模型，冷却后只放行一个 Half-Open 探测，成功或确定性响应关闭，瞬时失败重新计时。
6. 被放弃的响应体立即关闭；最终响应关闭时释放上游连接和关联 Context。

## 错误与可解释性

Provider 将本地构造错误标记为 `RequestError`，网络和 Context 错误标记为 `TransportError`。Router 使用 `UnsupportedModelError`、`NoAvailableTargetError` 和 `RouteError`，HTTP 层统一映射为 OpenAI 风格错误；已有的最终上游状态和正文继续透传。

响应头仅包含安全摘要：`X-Limen-Provider`、`X-Limen-Attempts`、`X-Limen-Route`。日志读取相同字段，不记录 API Key、上游模型、Prompt 或完整 Response。路径长度受每个模型最多四个目标限制。

## 健康与交付

`/livez` 只表示进程可响应；`/readyz` 表示启动依赖已完成，关闭时先变为未就绪再执行 `Server.Shutdown`。`limen version` 和 `limen healthcheck` 不读取业务密钥；Docker 使用静态非 root 运行时。完整运维说明见 [`docs/operations.md`](operations.md)。

## 明确不包含

本版本不实现热加载、远程配置、同目标重试、动态权重、随机负载均衡、成本路由、语义缓存、Prompt 分类、分布式熔断、数据库、管理后台或完整 Prometheus/OpenTelemetry 平台。
