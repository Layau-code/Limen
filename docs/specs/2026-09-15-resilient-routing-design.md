# Limen 可靠性路由与交付设计

## 1. 项目定位

Limen 不以“支持更多模型”为主要卖点，而是聚焦 Agent 请求在多模型环境中的可靠执行：一次请求必须遵守统一时间预算，发生瞬时故障时可以安全切换目标，并能够解释最终路由结果。

项目对外定位为：

> 面向 Agent 的、可解释且预算感知的多模型可靠性网关。

本阶段同时补齐健康检查、版本信息、容器镜像、持续集成和真实启动验证，使核心设计能够被实际交付。

## 2. 设计原则

1. 总请求预算优先于单次上游超时，切换 Provider 不得重新获得完整超时时间。
2. 每个目标最多调用一次，不对同一目标自动重试，避免重复计费和重试风暴。
3. 只有瞬时故障可以触发 Fallback，确定性请求错误和鉴权错误直接返回。
4. SSE 响应一旦开始向客户端传输，禁止切换 Provider。
5. 熔断状态只描述目标近期可用性，不参与 Prompt、业务用户或响应内容判断。
6. 路由过程可以观察，但日志和响应头不得包含密钥、Prompt 或完整响应。
7. 配置和旧代码直接演进到新结构，不长期维护两套模型配置语义。

## 3. 配置模型

模型文件升级为一个逻辑模型对应一组有序目标：

```json
{
  "routing": {
    "attempt_timeout": "10s",
    "failure_threshold": 3,
    "cooldown": "30s"
  },
  "models": [
    {
      "id": "smart-model",
      "display_name": "Smart Model",
      "targets": [
        {
          "provider": "openai",
          "upstream_model": "gpt-5-mini"
        },
        {
          "provider": "anthropic",
          "upstream_model": "claude-sonnet-4-20250514"
        }
      ]
    }
  ]
}
```

规则如下：

- `id` 和 `targets` 必填，`display_name` 可选。
- 每个逻辑模型包含一至四个目标，顺序即优先级。
- `provider` 和 `upstream_model` 必填，Provider 只允许 `openai` 或 `anthropic`。
- 同一逻辑模型内不允许重复的 Provider 与上游模型组合。
- `routing` 可省略；默认单次超时 `15s`、连续失败阈值 `3`、冷却时间 `30s`。
- `attempt_timeout`、`cooldown` 必须是正数，`failure_threshold` 必须大于零。
- `LIMEN_REQUEST_TIMEOUT` 继续表示完整请求的总时间预算，默认 `60s`；单次超时超过总预算时由父 Context 自动截断。
- JSON 使用严格字段校验，旧版 `provider`、`upstream_model` 顶层字段会得到明确启动错误。
- 只要求所有目标实际引用的 Provider Key。
- Provider Base URL 必须是绝对的 `http` 或 `https` URL，且不得包含用户信息。

未设置 `LIMEN_MODELS_FILE` 时继续启用前缀兼容模式。每条模式生成一个单目标逻辑模型，不执行跨 Provider Fallback。

## 4. 组件边界

```text
HTTP API
  -> Request Parse / Authentication
  -> Model Registry
  -> Budget-aware Router
       -> Circuit Breaker
       -> Target 1 Provider
       -> Target 2 Provider
  -> Route Decision Headers
  -> Response / SSE Relay
```

- `internal/config`：解析严格 JSON、路由默认值、模型目标和 Provider 配置。
- `internal/gateway/registry.go`：保存只读逻辑模型及有序目标，提供精确匹配和兼容模式解析。
- `internal/gateway/router.go`：管理总预算、单次尝试、Fallback 分类和最终路由结果。
- `internal/gateway/breaker.go`：实现进程内、按目标隔离的并发安全熔断状态机。
- `internal/provider`：只负责协议转换和一次上游调用；不再拥有请求总超时或 Fallback 策略。
- `internal/httpapi`：映射错误、写入路由响应头、转发响应，并保持 OpenAI-Compatible 边界。

Router 使用 Provider 名称到实例的映射。该映射服务于当前两个真实 Provider，不引入插件系统或动态 Provider 注册。

## 5. 请求与 Fallback 流程

1. Handler 完成鉴权和 OpenAI Chat Completions 请求解析。
2. Router 解析逻辑模型，并为完整调用创建总预算 Context；如果调用方已有更早截止时间，则以调用方为准。
3. Router 按顺序检查目标熔断状态。开放且尚未到冷却期的目标被跳过。
4. 对允许调用的目标创建单次超时 Context，替换真实上游模型并调用 Provider。
5. 收到 `2xx` 响应后立即返回。此后即使 SSE 读取失败也不再 Fallback。
6. 收到确定性非 `2xx` 响应时直接返回，并把目标视为可连接状态。
7. 收到瞬时状态或传输错误时记录熔断失败；如果仍有预算和下一个目标，则关闭当前响应并继续。
8. 所有目标用尽后，保留最后一个上游 HTTP 响应；若只有传输错误，则返回统一网关错误。

瞬时状态固定为：`408`、`409`、`429`、`500`、`502`、`503`、`504`、`529`。其他 `4xx` 和 `5xx` 不自动切换，避免在没有明确语义时扩大请求次数。客户端取消和总预算耗尽也不会继续尝试。

## 6. 错误类型

Provider 调用错误分为两类：

- `RequestError`：请求转换、URL 构造或其他本地确定性错误，不执行 Fallback。
- `TransportError`：连接、握手或读取响应头失败，可以执行 Fallback。

Router 额外提供：

- `UnsupportedModelError`：逻辑模型不存在，返回 `400 unsupported_model`。
- `NoAvailableTargetError`：所有目标都处于熔断状态，返回 `503 no_available_target`。
- `RouteError`：目标已耗尽且没有可转发的上游响应，根据原因返回 `502` 或 `504`。

上游已经返回的最终 HTTP 状态和正文继续透传。被放弃目标的响应体必须立即关闭。

## 7. 熔断状态机

熔断器按 `provider + upstream_model` 隔离，并在启动时随注册表创建。

```text
Closed
  -> 连续瞬时失败达到阈值 -> Open
Open
  -> 冷却时间未结束 -> 跳过目标
  -> 冷却时间结束 -> 只允许一个请求进入 Half-Open
Half-Open
  -> 成功或确定性响应 -> Closed
  -> 瞬时失败 -> Open，并重新开始冷却
```

实现使用小粒度互斥锁，不启动后台 goroutine。Half-Open 同一时间只允许一个探测请求，其余请求直接尝试后续目标。进程重启会清空状态，本阶段不引入 Redis 或跨实例协调。

## 8. 可解释路由

每次路由生成一个不含业务正文的决策记录，包含尝试的 Provider、结果分类、最终 Provider 和实际尝试次数。

HTTP 响应增加：

- `X-Limen-Provider`：最终响应来自的 Provider。
- `X-Limen-Attempts`：实际发出的上游请求数量。
- `X-Limen-Route`：紧凑路径，例如 `openai:429>anthropic:200` 或 `openai:circuit_open>anthropic:200`。

访问日志读取这些响应头并写入结构化字段。路径只包含 Provider 和结果类别，不包含上游模型、密钥、Prompt 或响应内容。路由头长度通过每个模型最多四个目标进行上界控制。

配置模式下，`GET /v1/models` 的 `owned_by` 返回 `limen`，因为一个逻辑模型可能跨越多个 Provider；兼容模式仍返回实际 Provider。

## 9. 健康与交付

- `GET /livez`：无需鉴权，只表示进程和 HTTP 服务可响应，返回最小 JSON。
- `GET /readyz`：无需鉴权，启动配置完成后返回成功；收到关闭信号后先标记未就绪，再执行优雅关闭。
- 健康接口不实时请求 Provider，避免外部故障导致实例反复重启或从负载均衡中震荡。
- `limen version` 输出构建版本、提交和构建时间。
- `limen healthcheck` 调用本地 `/readyz`，供无 Shell 的容器镜像执行。
- Docker 镜像使用多阶段静态构建和非 root 运行时，不包含源码、编译器或密钥。
- GitHub Actions 执行格式检查、`go vet`、竞态测试和构建。
- `make smoke` 启动真实二进制，验证健康检查、鉴权失败和模型列表后关闭进程。

## 10. 测试设计

配置测试覆盖：

- 多目标配置、默认路由参数和严格未知字段拒绝。
- 空目标、超过四个目标、重复目标、未知 Provider 和非法时长。
- 所有目标的按需 Provider Key 校验和 Base URL 校验。

Router 测试覆盖：

- 主目标成功时不调用备用目标。
- 瞬时 HTTP 状态和传输错误触发 Fallback。
- 确定性错误、客户端取消和总预算耗尽不触发 Fallback。
- 多次尝试共享总截止时间，单次超时不会重置总预算。
- SSE 返回 `2xx` 后读取失败也不调用备用目标。
- 最终上游响应、响应体关闭和路由决策正确。

熔断测试使用可控时钟，不通过真实休眠验证：

- 达到阈值后打开、冷却前拒绝、冷却后 Half-Open。
- 并发 Half-Open 只放行一个请求。
- 成功、确定性响应和瞬时失败对应正确状态迁移。
- `go test -race` 验证状态并发安全。

HTTP 与交付测试覆盖：

- 路由头、OpenAI 错误结构、健康接口和关闭时就绪状态。
- 日志不包含 Key、Prompt、响应正文或上游模型。
- 普通响应与 SSE 的 OpenAI、Anthropic 回归测试。
- Docker 构建和真实二进制冒烟测试。

## 11. 明确不包含

本阶段不实现相同目标自动重试、动态权重、随机负载均衡、成本路由、语义缓存、Prompt 分类、分布式熔断、配置热加载、数据库、管理后台或完整遥测平台。

这些能力只有在当前路由语义和交付基线稳定后，基于新的真实需求单独设计。

## 12. 验收标准

- 示例中的双目标逻辑模型能够在主目标瞬时失败时切换到备用 Provider。
- 总时间预算、单次超时、取消传播和 SSE 不可重放边界有自动化证据。
- 熔断器在并发与冷却场景下行为确定，竞态测试通过。
- 客户端可以从响应头和日志解释最终路由过程，且敏感信息测试通过。
- `make check`、`make smoke`、独立二进制构建和 Docker 镜像构建通过。
- 新用户只参考 README 和示例配置即可启动 Limen 并完成一次模型列表请求。
