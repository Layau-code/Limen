# Limen 运维说明

## 启动

生产环境至少设置 `LIMEN_API_KEY`，以及模型文件实际引用的 `OPENAI_API_KEY` 或 `ANTHROPIC_API_KEY`。`LIMEN_MODELS_FILE` 只在启动时读取，修改后重启进程。默认监听 `:8080`，可用 `LIMEN_ADDR` 修改。启用 PostgreSQL Key Store 时，首个 `admin` Key 必须由部署初始化流程预置；之后可通过 `POST /v1/limen/keys` 创建后续 Key。

启动失败通常表示配置错误：检查 JSON 是否严格匹配示例、模型 ID 是否重复、目标 Provider 是否支持、时长是否为正数，以及实际引用的 Provider Key 是否存在。Base URL 必须是绝对 HTTP(S) 地址且不能含用户信息。

## 健康检查

- `GET /livez`：无需鉴权，进程可响应即返回 `200`。
- `GET /readyz`：无需鉴权，依赖组装完成返回 `200`；关闭流程开始后返回 `503`。
- `limen healthcheck`：请求本地 `/readyz`，可直接作为容器健康检查命令。

健康接口不主动请求 Provider，避免外部模型故障导致实例反复重启。

## 关闭与日志

收到 `SIGINT` 或 `SIGTERM` 后，服务先标记未就绪，再等待已有请求在最多 10 秒内完成并关闭 HTTP Server。日志使用 JSON 格式，包含 request ID、方法、固定路由类别、状态、耗时、TTFB 和安全路由摘要；不会记录鉴权头、API Key、Prompt、响应正文、动态路径或上游模型名。

`X-Limen-Route` 例如 `openai:503>anthropic:200`，可用于解释是否发生 Fallback。`X-Limen-Attempts` 是实际发出的上游请求数，不代表同一目标重试次数。

## Trace

设置 `OTEL_EXPORTER_OTLP_ENDPOINT` 或 `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` 后启用 OTLP/HTTP Trace，认证头使用 `OTEL_EXPORTER_OTLP_HEADERS`。Limen 接受并继续传播 W3C `traceparent`，不传播 Baggage。一次受治理请求可以沿同一 Trace 查看 HTTP、Run 准入、Decision、每个 Provider Attempt 和 Settlement；Span 不包含 Prompt、Response、密钥、原始错误正文或上游模型名。

Exporter 在后台批量发送。初始化失败会禁用 Trace，运行时导出失败只记录不含端点和凭据的通用告警，不影响模型请求。进程关闭时最多等待 5 秒刷新 Trace。

`GET /metrics` 需要 `admin` Scope。排障时，`limen_provider_attempts_total` 表示实际发出的上游调用；`circuit_open` 等未调用步骤只保留在 Decision 和 Trace 中。模型、状态和原因均为归一化低基数标签，不应使用它恢复原始请求内容。

请求日志和 HTTP Trace 的 `ttfb_ms` 表示首次写出响应正文的延迟；SSE 首段已 Flush 时即可观察该值，不代表完整响应或结算已经结束。

## 常见排障

1. `401 invalid_api_key`：检查客户端是否发送 `Authorization: Bearer <LIMEN_API_KEY>`。
2. `400 unsupported_model`：配置模式下检查请求模型是否精确匹配注册表 ID；兼容模式下检查四种前缀。
3. `503 no_available_target`：所有目标正在熔断，等待冷却或查看上游恢复情况。
4. `504 provider_timeout`：检查 `LIMEN_REQUEST_TIMEOUT` 与 `routing.attempt_timeout`，总预算不会因切换目标而重置。
5. 无 Fallback：确定性 4xx、请求转换错误、客户端取消和已开始的 SSE 都按设计不切换。
6. PostgreSQL 暂时不可用：事务连接池的单次网络 I/O 最多等待 5 秒；已持久化的结算任务在连接恢复后继续处理，未完成且租约过期的请求进入 `suspended_accounting`，不会自动重放 Provider。
7. API Key 创建响应丢失：出于安全边界，Limen 不从数据库恢复明文；使用仍有效的管理员 Key 创建新 Key，并撤销无法确认是否交付的旧 Key。

## 交付检查

提交前运行 `go clean -testcache && make check && make integration && make build && make smoke && make bench && git diff --check`。Docker 可用时再运行 `make image`，不要把真实 Provider Key 写入脚本或 CI。
