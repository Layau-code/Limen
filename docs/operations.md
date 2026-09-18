# Limen 运维说明

## 启动

生产环境使用静态鉴权时至少设置 `LIMEN_API_KEY` 或 `LIMEN_API_KEY_FILE`，并为模型文件实际引用的 Provider 设置对应环境密钥或只读文件密钥（例如 `OPENAI_API_KEY_FILE`）；启用 PostgreSQL Key Store 时，首个 `admin` Key 必须由部署初始化流程预置，`LIMEN_API_KEY_FILE` 不参与认证。文件密钥只在启动时读取，去除首尾空白；明文变量与对应 `_FILE` 同时设置、文件为空或文件不可读都会让启动失败。若同时设置 `LIMEN_CREDENTIAL_MASTER_KEY`，Provider 凭据必须在加密凭据存储中按租户配置，环境密钥不再作为租户请求的回退。`LIMEN_MODELS_FILE` 只在启动时读取，修改后重启进程。默认监听 `:8080`，可用 `LIMEN_ADDR` 修改；之后可通过 `POST /v1/limen/keys` 创建后续 Key，并使用轮换接口在不中断租户配置的情况下替换旧 Key。

启动失败通常表示配置错误：检查 JSON 是否严格匹配示例、模型 ID 是否重复、目标 Provider 是否支持、时长是否为正数，以及实际引用的 Provider Key 是否存在。生产 Provider Base URL 必须是绝对 HTTPS 地址且不能含用户信息；测试代码注入的 `httptest` Client 不受此配置限制。

## 健康检查

- `GET /livez`：无需鉴权，进程可响应即返回 `200`。
- `GET /readyz`：无需鉴权，依赖组装完成且监听 socket 已成功绑定后返回 `200`；监听失败或关闭流程开始后返回 `503`/不可连接。
- `limen healthcheck`：请求本地 `/readyz`，可直接作为容器健康检查命令；设置 `LIMEN_HEALTH_URL` 可覆盖检查地址，默认根据 `LIMEN_ADDR` 推导回环地址。
- `limen validate --models <path>`：离线检查模型目录、Provider endpoint 绑定并输出 `config_version`，不读取密钥或访问网络；可用 `--openai-base-url`、`--anthropic-base-url` 覆盖默认地址。
- `limen diff --base <path> --candidate <path>`：离线比较两个模型目录的结构影响，不读取密钥、不连接数据库、不访问 Provider。

健康接口不主动请求 Provider，避免外部模型故障导致实例反复重启。

## 离线演示

执行 `make demo` 或 `bin/limen demo` 可运行确定性演示。它先用 `model=auto` 和能力契约选中满足 `internal` 数据等级与质量下限的逻辑模型，并输出被排除目标的稳定原因码；再使用固定 Provider：第一个目标返回 `503`，第二个目标成功，最后把历史决策重放到草稿目录，并模拟一次 Run 准入、决策绑定和已知成本结算，输出选模、Fallback、影响分析和 Run 状态摘要。演示不读取环境密钥、不连接数据库、不访问外部网络，适合本地验收和文档截图。

执行 `bin/limen explain --models models.json --request chat-request.json` 可对本地配置和请求快照做离线路由解释。命令使用固定评估时间，连续执行得到相同的计划哈希；输出只包含候选原因、Provider、opaque 目标引用和哈希，不包含 Prompt、密钥或真实上游模型名。没有可用目标时仍返回 `no_eligible_target` 和每个候选的淘汰原因。

执行 `bin/limen validate --models models.json` 可在发布前检查模型目录格式和 endpoint 绑定，并输出稳定的配置版本和 Provider 数量摘要。该命令不读取业务密钥、不连接数据库、不创建 Provider Client；校验失败（包括 endpoint 错绑）时返回非零退出码，适合放入 CI 或容器构建步骤。

执行 `bin/limen diff --base models.current.json --candidate models.next.json` 可在审批前查看配置影响。输出只包含两个配置版本、是否发生变化、排序后的路径和变化类型；目标路径使用 opaque 引用，真实上游模型名、endpoint 原值和密钥不会出现在输出中。CI 可增加 `--fail-on provider,endpoint`，命中高风险映射变化时以非零退出码阻止发布，同时保留安全 JSON 供流水线归档。

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
7. API Key 创建或轮换响应丢失：出于安全边界，Limen 不从数据库恢复明文；使用仍有效的管理员 Key 再次执行新的幂等操作，并撤销无法确认是否交付的旧 Key。轮换提交后旧 Key 不再有效。

## 交付检查

提交前运行 `go clean -testcache && make check && make integration && make build && make smoke && make bench && git diff --check`。其中 `make check` 还会验证 `cmd` 和 `internal` 中每个生产方法都有简体中文用途注释。`make smoke` 使用临时二进制和占位密钥，验证 `/readyz`、Bearer 鉴权、OpenAI 风格 `/v1/models`、未知模型错误不会回显请求模型，以及收到 `SIGTERM` 后就绪状态撤销并正常退出；脚本不访问真实 Provider。Docker 可用时再运行 `make image`，不要把真实 Provider Key 写入脚本或 CI。
