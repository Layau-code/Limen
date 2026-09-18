# Limen

Limen（拉丁语：门槛、入口）是一个面向 Agent 的 Go AI Gateway。它提供 OpenAI 兼容接口，把稳定的逻辑模型名路由到 OpenAI 或 Anthropic，并在瞬时故障时按顺序切换备用目标。

```text
Agent / 应用 → Limen API Key → 模型注册表 → 预算感知路由 → Provider → 普通响应 / SSE
```

## 项目亮点

- 逻辑模型名与真实上游模型解耦，配置版本不可修改；启动文件用于引导，数据库发布版本可在运行中原子切换。
- `model=auto` 可根据能力契约、数据等级、质量和成本策略生成可解释的执行计划。
- Decision Engine 与 Gateway Executor 分离：前者只生成可重放计划，后者只按计划执行 Provider，不在执行阶段偷偷改变路由策略。
- 每次 Dry Run 和真实 Chat 都生成不含 Prompt/Response 的 Decision Journal，返回 `decision_id`，可 Explain 查询并 Replay 校验 `plan_hash`。
- 仓库提交 100 组完整 DecisionInput 与预期 `plan_hash`，重建 Engine 后逐组验证规范 ExecutionPlan 字节，防止算法漂移悄悄改写历史决策。
- 阶段 B 已加入 Run 领域状态机、幂等哈希和 PostgreSQL Store 迁移；HTTP 控制面接入前，现有无 Run Chat 行为保持不变。
- 受治理 Chat 的 Request 在准入后持有 30 秒租约并每 10 秒续租；实例崩溃后不重放 Provider 调用，而是标记未知费用并暂停 Run，避免重复计费。
- 崩溃恢复会把遗留的 `Attempt started` 原子标记为 `abandoned`；PostgreSQL 事务连接池具有 5 秒 I/O 期限，网络黑洞不会永久阻塞恢复任务。
- 每次真实 Provider 调用都保存独立 Attempt；若上游返回 request ID，Limen 会在结算前补写该非敏感标识，便于审计调用是否已经发生。
- Run 取消会写入租户隔离的取消事件；PostgreSQL 实例优先通过 `LISTEN/NOTIFY` 低延迟广播，在途 Chat 同时保留每秒轮询作为断线兜底。
- 鉴权边界生成不携带原始 Key 的租户 Principal，并按 Scope 控制数据面与 Run 控制面；默认使用环境变量静态 Key，也可切换 PostgreSQL Key Store。
- 可选 PostgreSQL API Key Store 只读取公开前缀、HMAC-SHA-256 摘要、租户和 Scope；完整 Key 不落库。启用后管理员可创建、列出和撤销 Key，明文只在创建首次响应中返回；默认仍使用静态 Key 便于单机开发。
- Provider 凭据可选使用 AES-GCM 加密存储，密文绑定租户、Provider 和 endpoint；Provider 密钥轮换不会打断在途请求。
- 一次请求共享总时间预算；每个目标最多调用一次，避免重试风暴和重复计费。
- 仅对 Provider 归一化的 `retryable_transient` 和传输错误执行 Fallback；SSE 开始后不重放。
- 进程内并发安全熔断器、可解释路由响应头和不记录敏感正文的结构化日志。
- Provider 用量采集与按目标价格的定点成本结算；普通响应和 SSE 都保持实时转发。
- 核心 HTTP 数据面不使用 Web 框架或 ORM；外部依赖只用于 PostgreSQL 与 OpenTelemetry 等明确边界，并包含竞态测试、真实二进制冒烟测试、Docker 和 CI 资产。
- `/metrics` 提供固定指标和有界标签，必须使用 `admin` Scope，避免把请求标识和正文带入观测系统。
- 可选 OTLP/HTTP Trace 把 HTTP、Run 准入、Decision、每次 Attempt 和 Settlement 串成同一证据链；只传播 `traceparent`，不记录正文、密钥或上游模型名。
- 结构化日志和 HTTP Trace 记录安全的 `ttfb_ms`，可区分 SSE 首段延迟与完整响应/结算延迟。
- 控制面变更写入租户隔离的安全审计摘要，`GET /v1/limen/audit` 仅允许 `admin` Scope，事件不含正文、密钥或真实上游模型名。

## 快速开始

要求 Go 1.24+。先准备 `configs/models.example.json` 格式的模型文件：

```json
{
  "routing": {"attempt_timeout": "10s", "failure_threshold": 3, "cooldown": "30s"},
  "models": [{
    "id": "smart-model",
    "display_name": "Smart Model",
    "targets": [
      {
        "id": "openai-primary",
        "provider": "openai",
        "upstream_model": "gpt-5-mini",
        "capabilities": ["text"],
        "supports_streaming": true,
        "quality_tier": 4,
        "cost_tier": 1,
        "context_window": 128000,
        "data_classes": ["public", "internal", "confidential"],
        "pricing": {"input_per_million_usd": "0.250000", "output_per_million_usd": "2.000000"}
      },
      {
        "id": "anthropic-primary",
        "provider": "anthropic",
        "upstream_model": "claude-sonnet-4-20250514",
        "capabilities": ["text"],
        "supports_streaming": true,
        "quality_tier": 5,
        "cost_tier": 2,
        "context_window": 200000,
        "data_classes": ["public", "internal", "confidential"],
        "pricing": {"input_per_million_usd": "3.000000", "output_per_million_usd": "15.000000"}
      }
    ]
  }]
}
```

使用二进制启动：

```bash
make build
export LIMEN_API_KEY=local-limen-key
export LIMEN_MODELS_FILE=/absolute/path/to/models.json
export OPENAI_API_KEY=your-openai-key
export ANTHROPIC_API_KEY=your-anthropic-key
./bin/limen
```

也可以使用 Docker：

```bash
docker build -t limen:dev .
docker run --rm -p 8080:8080 \
  -e LIMEN_API_KEY=local-limen-key \
  -e OPENAI_API_KEY=your-openai-key \
  -e ANTHROPIC_API_KEY=your-anthropic-key \
  -e LIMEN_MODELS_FILE=/etc/limen/models.json \
  -v "$PWD/configs/models.example.json:/etc/limen/models.json:ro" limen:dev
```

调用时使用逻辑模型名：

```bash
curl http://localhost:8080/v1/chat/completions \
  -H 'Authorization: Bearer local-limen-key' \
  -H 'Content-Type: application/json' \
  -d '{"model":"smart-model","messages":[{"role":"user","content":"Hello"}]}'
```

`GET /v1/models` 与聊天接口共用 Bearer Key 鉴权。配置模式的 `owned_by` 为 `limen`；未配置模型文件时进入兼容模式，支持 `gpt-*`、`o1-*`、`o3-*` 和 `claude-*`，并要求两个 Provider Key。

配置版本可以通过控制面创建和发布。`POST /v1/limen/configs` 接收与模型文件相同的严格 JSON，返回由规范内容生成的 `version`；`POST /v1/limen/configs/{version}/publish` 需要 `configs:write` 和 `Idempotency-Key`，同键重试不会重复切换版本，不同版本复用同键会返回 `idempotency_conflict`。发布后当前进程立即使用新目录和路由参数，旧版本保留为 `superseded`。配置发布通过 PostgreSQL `NOTIFY` 加速传播到其他实例，实例仍每 5 秒读取已发布版本作为丢失通知时的兜底；通知只包含租户和版本哈希，不包含配置正文。`GET /v1/limen/configs` 只返回模型和目标摘要，不暴露真实上游模型名；`GET /v1/limen/configs/{version}/diff/{base_version}` 返回只含路径和变化类型的结构化差异。配置 API 未连接 PostgreSQL 时使用内存存储，重启会丢失版本；生产环境应配置 `LIMEN_DATABASE_URL`。

管理员可通过 `GET /v1/limen/audit?limit=100` 查询当前租户最近的控制面变更摘要。返回内容只包括动作、资源类型、资源 ID、结果、请求哈希和时间；`limit` 范围为 1 到 100。

启用 `LIMEN_API_KEY_STORE=postgres` 后，管理员可以使用 `POST /v1/limen/keys` 创建 Key、`GET /v1/limen/keys` 查看元数据，以及 `POST /v1/limen/keys/{public_prefix}/revoke` 撤销 Key。创建请求必须带 `Idempotency-Key` 和明确的 `scopes`；完整 `lmn_live_...` Key 只在首次创建响应返回，重试不会再次返回明文。数据库只保存 HMAC 摘要，认证查询通过受控函数执行，管理查询受 PostgreSQL RLS 保护。

配置模式下客户端只能使用注册表中的逻辑模型 ID。也可以使用 `model=auto`，并在请求的可选 `limen` 对象中声明 `required_capabilities`、`minimum_quality_tier`、`required_context_tokens`、`data_class` 和 `strategy`（`balanced` 或 `economy`）；受治理 Run 创建时固定的策略优先，冲突请求返回 `400 strategy_conflict`。当前仅支持文本消息和流式文本，Tools、Vision、Responses API 等字段会明确返回 `400 unsupported_field`。

可以调用 `POST /v1/limen/decisions/dry-run` 使用同一请求格式只生成执行计划，不访问 Provider、不计入用量；返回内容包含候选目标、淘汰原因和 `input_hash`/`plan_hash`，适合在 Agent 调用前解释路由选择。

决策记录可通过 `GET /v1/limen/decisions/{decision_id}` 查询，或调用 `POST /v1/limen/decisions/{decision_id}/replay` 使用历史输入重新生成计划。Replay 不访问 Provider、不读取当前熔断状态，只返回原计划、重放计划、`match` 和结构化差异（策略、目标顺序、候选原因和哈希）；差异中不包含真实上游模型名。真实 Chat 与 Dry Run 会在响应头返回 `X-Limen-Decision-ID`；决策记录只包含模型名、能力契约、候选目标和哈希，不保存 Prompt 或 Response。

`internal/decision/testdata/fixtures.json` 保存 100 组版本化 Replay 语料，覆盖契约、数据等级、流式、上下文、健康状态、预算和排序。当前新请求使用 `decision.v2`：无序能力集合会排序去重，显式模型的 Fallback 目标优先级保持不变；`decision.v1` 仍注册用于历史 Replay。算法注册表支持为旧版本设置 `retainUntil`，超过保留截止时间后返回 `algorithm_version_unavailable`，不会静默使用新算法。`go generate ./internal/decision` 可确定性重建文件；测试要求数量不能减少，且规范计划字节和已提交哈希都保持一致。

启用开发用 Run Store 后可使用 `POST /v1/limen/runs`、`GET /v1/limen/runs/{run_id}`、`POST /v1/limen/runs/{run_id}/complete`、`POST /v1/limen/runs/{run_id}/cancel` 和请求状态查询。Run 请求必须带 `X-Limen-Run-ID` 与 `Idempotency-Key`；同一键不会重复调用 Provider，结算状态通过请求查询作为事实来源。取消 Run 后，在途请求会收到 `409 run_cancelled`，并由租户取消事件传播到其他实例。

未知费用会让 Run 进入 `suspended_accounting`，不会伪造为零成本，也不会自动重放上游请求。管理员可用 `POST /v1/limen/runs/{run_id}/requests/{request_id}/accounting` 处理：`{"mode":"cost","cost_usd":"0.001"}` 补记确定金额，或 `{"mode":"accept_unknown"}` 明确接受未知费用；接口需要 `admin` Scope 和独立 `Idempotency-Key`，处理后请求进入 `settled`，结算状态分别为 `complete` 或 `unknown`。

暂停期间仍可请求完成 Run；Limen 只记录 `complete_requested`，待同一 Run 的未知费用全部处置后再完成，不会绕过账本状态机。

成功或最终上游响应会带有以下安全摘要：

```text
X-Limen-Provider: anthropic
X-Limen-Attempts: 2
X-Limen-Route: openai:503>anthropic:200
```

如果目标配置了 `pricing`，响应结束后还会通过 HTTP Trailer 和结构化日志提供 `input_tokens`、`output_tokens`、`total_tokens`、`cost_usd` 和 `settlement_status`。SSE 内容仍然逐块推送，不会等待完整响应；如果上游没有返回用量或客户端提前断开，费用字段会留空。受治理请求的结算遇到暂时性存储错误时，会在当前进程内按短退避重试；仍未完成时返回 `pending`，租约过期后会被回收为未知费用并暂停 Run，避免重复调用。

价格字段使用每百万 Token 的美元字符串，输入价和输出价必须同时填写。当前版本只负责请求结束后的结算，不实现每日额度或超额拦截。

Run 预算采用事后软阈值：已开始请求允许完成，结算后达到阈值才阻止后续请求；不设置单请求金额上限。阶段 B 的持久化边界使用租户组合键、RLS 和 `ledger_entries(tenant_id, request_id)` 唯一约束，结算未知时保留 `pending` 并暂停 Run 记账。请求租约默认 30 秒、每 10 秒续租；结算存储失败会进入持久化 `settlement_jobs`，过期恢复不自动重放可能已经产生费用的上游调用。

PostgreSQL 迁移还会对租户表启用 `FORCE ROW LEVEL SECURITY`，即使表所有者路径也不能绕过租户策略；需要运维操作时应使用独立的数据库角色。

## 配置与运维

环境变量包括 `LIMEN_ADDR`（默认 `:8080`）、`LIMEN_API_KEY`、`LIMEN_API_KEY_STORE`（`static` 或 `postgres`，默认 `static`）、`LIMEN_API_KEY_HMAC_SECRET`（PostgreSQL Key Store 必填）、`LIMEN_API_SCOPES`（静态 Key 可选，逗号分隔，默认全部 Scope）、`LIMEN_MODELS_FILE`、`OPENAI_API_KEY`、`OPENAI_BASE_URL`、`ANTHROPIC_API_KEY`、`ANTHROPIC_BASE_URL`、`LIMEN_REQUEST_TIMEOUT`（默认 `60s`）、`LIMEN_DATABASE_URL`（可选 PostgreSQL DSN）、`LIMEN_TENANT_ID`（默认 `local`）和 `LIMEN_CREDENTIAL_MASTER_KEY`（可选，32 字节十六进制/Base64/原文主密钥）。配置数据库后，启动会 Ping 数据库并执行版本化迁移，使用 PostgreSQL 持久化 Run、Request、Attempt、Ledger、待结算任务、Decision Journal、配置版本、API Key 摘要和控制面操作；启动日志不会输出 DSN。PostgreSQL Key Store 模式要求同时配置数据库和 HMAC Secret，API Key 格式为 `lmn_live_<public_prefix>_<random_secret>`，Key 由 `admin` 控制面按需创建。设置凭据主密钥后，启动会按租户和 endpoint 读取加密 Provider 凭据；未找到时回退到对应 Provider 环境变量。

设置标准环境变量 `OTEL_EXPORTER_OTLP_ENDPOINT` 或 `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` 即可启用 OTLP/HTTP Trace；认证头可使用 `OTEL_EXPORTER_OTLP_HEADERS`。未配置端点时使用无操作 Provider。导出在后台批量执行，初始化或导出失败只关闭或降级遥测，不改变模型请求响应。

启用数据库和 `LIMEN_CREDENTIAL_MASTER_KEY` 后，管理员可使用 `POST /v1/limen/credentials/{provider}` 轮换 Provider 凭据，或调用 `POST /v1/limen/credentials/{provider}/revoke` 撤销。请求必须提供匹配当前配置的 `endpoint_id`，响应只返回凭据元数据，不返回密钥；endpoint ID 可由 `provider.EndpointIDForBaseURL` 生成。凭据轮换在当前实例立即生效，其他实例通过 PostgreSQL `NOTIFY` 刷新；通知故障不会回滚数据库变更，实例可重启重新加载。

静态 Key 支持 `inference`、`runs:read`、`runs:write`、`decisions:read`、`configs:read`、`configs:write` 和 `admin`。Chat/Models 需要 `inference`；Dry Run 需要 `inference,decisions:read`；Run 创建、完成、取消以及带 `X-Limen-Run-ID` 的 Chat 需要 `runs:write`；Run 和 Request 查询需要 `runs:read`。鉴权通过后下游只接收租户 Principal，不读取原始 Key。

未知费用处置接口需要 `admin`，并且必须携带 `Idempotency-Key`；它只返回 Request 状态，不返回 Prompt、Response 或 Provider 凭据。

`GET /metrics` 需要 `admin`，输出固定计数器 `limen_chat_requests_total`、`limen_provider_attempts_total`、`limen_settlements_total`，以及请求耗时和 TTFB 直方图 `limen_chat_request_duration_seconds`、`limen_chat_ttfb_seconds`。直方图使用固定桶，不接受非法或无穷值。模型标签只使用已注册逻辑 ID、`auto` 或 `gpt-*` 等兼容模式，未知输入统一为 `unsupported`；状态只使用 `2xx/4xx/5xx` 等类别，拒绝原因只使用稳定错误码。Attempt 只统计真实 Provider 调用，熔断跳过不会虚增。标签不包含 Request ID、Run ID、租户 ID、Prompt、Response、密钥或原始错误。

`/livez` 表示进程存活，`/readyz` 表示已完成启动；`limen version` 输出版本信息，`limen healthcheck` 检查本地就绪状态。更多关闭流程、日志和排障说明见 [`docs/operations.md`](docs/operations.md)。

## 开发验证

`make check` 运行格式、静态分析和竞态测试；`make integration` 使用临时 PostgreSQL 17 容器验证 RLS、100 并发准入、幂等、唯一账本、强制终止执行进程、数据库暂停/恢复、租约竞争以及取消通知/轮询。集成测试也可通过 `LIMEN_TEST_DATABASE_ADMIN_URL`、`LIMEN_TEST_DATABASE_URL` 和 `LIMEN_TEST_DATABASE_ROLE` 使用外部测试数据库，三个变量必须同时提供；此时无法安全控制数据库生命周期的暂停场景会跳过。

```bash
make check   # gofmt、go vet、竞态测试
make integration # 真实 PostgreSQL 并发、RLS 与恢复测试
make smoke   # 真实二进制启动与 API 冒烟
make bench   # Router 主路径与 Fallback 基准
```

本机 Apple M5、darwin/arm64 的近期基准大致为：主路径 `5–6 μs/op`、59 次分配；Fallback 路径 `6–8 μs/op`、70 次分配。该数字包含未启用导出时的 Trace 边界，只用于描述测量环境，不构成性能承诺。

设计决策见 [`docs/design.md`](docs/design.md)，开发规范见 [`AGENTS.md`](AGENTS.md)，变更记录见 [`CHANGELOG.md`](CHANGELOG.md)。
