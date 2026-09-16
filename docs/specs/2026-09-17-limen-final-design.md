# Limen 最终版本设计

## 1. 定位与目标

Limen 是面向 Agent 工作流的可解释模型决策网关。

它接收 OpenAI 兼容请求，允许客户端声明本次任务需要的能力和约束；Limen 根据整个 Agent Run 的预算、截止时间、模型能力、运行健康状态和历史尝试动态选择上游模型，并为每次选择提供可审计、可回放的依据。

Limen 的核心不是支持尽可能多的 Provider，而是把一次 Agent 任务作为治理单位：

~~~text
能力契约
  → Run 级决策
  → 能力安全的可靠执行
  → 请求结束后结算
  → 决策解释与回放
~~~

最终版本需要同时满足：

- 现有 OpenAI SDK 可以通过修改 base_url 使用。
- Agent 可以跨多个模型请求共享预算、截止时间和策略。
- Fallback 不会选择缺少必要能力的模型。
- 每次决策能回答“为什么选它、为什么跳过其他目标”。
- 相同的决策快照和算法版本可以得到相同的路由结果。
- Prompt、Response、API Key 和 Provider 凭据默认不被持久化或写入日志。
- 代码、注释、测试和文档保持简洁、可读，正文使用简体中文。

## 2. 设计原则

1. 先保证协议和可靠性，再增加智能策略。Provider 只负责协议适配，路由和治理由上层完成。
2. 能力契约优先于模型名称。Agent 依赖能力和约束，不直接依赖某个厂商模型。
3. 决策必须可解释。使用有序过滤和字典序排序，不使用无法说明来源的黑盒综合分数。
4. 预算只在请求结束后结算。不为了费用控制截断已开始的普通响应或 SSE；超额只阻止后续请求。
5. 未知值不伪装成零。缺少 Usage、价格或完整账本时，明确记录不完整状态。
6. 数据面保持无状态。强一致状态放入 PostgreSQL，实例内健康状态只作为带快照的运行输入。
7. 抽象由真实需求驱动。不提前设计万能协议、Provider 插件市场或复杂策略 DSL。
8. 新增生产方法必须带简体中文用途注释。注释说明职责、边界或非显然原因，命名和拆分优先保证可读性。

## 3. 总体架构

最终形态为单个 Go 二进制的模块化单体，可水平扩展：

~~~text
Agent / OpenAI SDK
        ↓
HTTP API、鉴权、租户边界
        ↓
请求标准化与能力契约
        ↓
Run Coordinator（准入、状态、预算）
        ↓
Decision Engine（筛选、排序、解释）
        ↓
Gateway Executor（超时、熔断、Fallback、流式）
        ↓
Provider Adapter（协议转换）
        ↓
OpenAI / Anthropic / Gemini / OpenAI-Compatible
        ↓
Usage 结算、Ledger、Decision Journal
~~~

推荐的内部边界：

~~~text
internal/auth       Principal、Scope、租户隔离
internal/config     环境变量、配置导入和校验
internal/catalog    模型能力目录和不可变版本
internal/decision   纯决策、Explain、Dry Run、Replay
internal/run        Run 生命周期、准入和结算协调
internal/gateway    可靠性执行、熔断、Fallback、流式转发
internal/provider   上游协议适配和 Usage 采集
internal/store      PostgreSQL 接口、迁移和事务
internal/cost       定点价格和成本计算
internal/httpapi    HTTP 边界、错误映射和响应头
~~~

依赖方向保持单向：

~~~text
httpapi → run → decision / gateway → provider
   └────→ auth / catalog / store
~~~

Decision Engine 不访问数据库、不访问网络、不调用 Provider；所有外部状态先形成快照再传入。Gateway Executor 不依赖 HTTP 框架对象。Provider 不知道 Run、预算和逻辑模型映射。

## 4. 调用模型与 Run 生命周期

### 4.1 无状态兼容调用

没有 X-Limen-Run-ID 时，保留当前单请求行为：请求独立鉴权、解析逻辑模型、执行并在响应结束后结算。现有客户端无需理解 Run，也不受 PostgreSQL 账本状态影响。

### 4.2 受治理调用

客户端先创建 Run：

~~~http
POST /v1/limen/runs
Authorization: Bearer <limen-key>
~~~

~~~json
{
  "budget_usd": "1.000000000",
  "deadline": "2026-09-17T18:00:00+08:00",
  "max_parallelism": 4,
  "strategy": "balanced"
}
~~~

聊天或 Responses 请求通过 X-Limen-Run-ID Header 关联。

Run 创建时固定模型目录版本、策略版本和价格版本。模型实时健康状态不固定，但每次决策保存实际使用的健康快照。全局模型封禁、密钥撤销和安全策略可以覆盖 Run 的固定配置。

### 4.3 Run 状态

~~~text
active
  ├── completed
  ├── cancelled
  ├── deadline_exceeded
  ├── budget_exhausted
  └── accounting_uncertain
~~~

只有 active 状态允许开始新请求。

- 当前请求开始后，即使结算后预算超额，也允许其正常完成。
- 结算导致累计金额达到或超过预算时，Run 变为 budget_exhausted。
- Usage 或价格无法确认时，受治理 Run 变为 accounting_uncertain，暂停后续请求。
- 截止时间和客户端取消属于执行控制，可以取消在途上游请求。
- 费用控制不截断已开始的普通响应或 SSE。

### 4.4 并发语义

准入事务只检查已经结算的累计成本。多个已获准请求可以并发完成，因此最终金额可能超过预算；这是为了保持请求完成后结算和流式响应完整性。

max_parallelism 只限制在途请求数量，不是单请求费用上限。Run 行锁只覆盖准入或结算事务，不覆盖网络调用。

单个受治理 Run 的流程：

~~~text
鉴权
  → 锁定 Run 并检查准入
  → 解析能力契约和固定配置
  → 获取健康快照
  → 生成候选和决策记录
  → 执行有序目标与安全 Fallback
  → 实时发送响应
  → 响应结束后结算 Usage 和成本
  → 更新 Ledger、Request 和 Run 状态
~~~

## 5. 能力契约与模型目录

### 5.1 请求扩展

完全兼容的请求仍可指定逻辑模型：

~~~json
{
  "model": "smart-model",
  "messages": []
}
~~~

Agent 可以使用 Limen 扩展：

~~~json
{
  "model": "auto",
  "messages": [],
  "limen": {
    "task": "coding",
    "step": "final_answer",
    "required_capabilities": ["reasoning", "tool_calling"],
    "minimum_quality_tier": 4,
    "required_context_tokens": 32000,
    "estimated_input_tokens": 4000,
    "estimated_output_tokens": 1200,
    "max_estimated_cost_usd": "0.050000000",
    "max_latency_ms": 10000,
    "data_class": "internal",
    "strategy": "balanced"
  }
}
~~~

limen 通过 OpenAI SDK 的 extra_body 传入。Run ID 使用 Header，避免私有字段被转发给 Provider。

硬约束包括能力、上下文、流式/工具支持和数据等级；质量、成本、延迟是排序偏好。没有 Token 估算时不猜测 Tokenizer，只能使用配置的 cost_tier 排序；设置 max_estimated_cost_usd 却不提供估算值时返回校验错误。

### 5.2 目标目录

能力属于具体目标，而不是 Provider：

~~~json
{
  "id": "coding-strong",
  "display_name": "Coding Strong",
  "targets": [
    {
      "id": "anthropic-claude-code",
      "provider": "anthropic",
      "upstream_model": "claude-sonnet",
      "capabilities": [
        "text", "reasoning", "coding", "tool_calling",
        "structured_output", "long_context"
      ],
      "quality_tier": 4,
      "cost_tier": 3,
      "context_window": 200000,
      "data_classes": ["public", "internal"],
      "pricing": {
        "input_per_million_usd": "3.000000",
        "output_per_million_usd": "15.000000"
      }
    }
  ]
}
~~~

初期能力使用受控枚举：text、vision、reasoning、coding、tool_calling、structured_output、long_context。能力和质量等级是版本化配置声明，不伪装成自动评测结论。

对于受治理 Run，缺少完整价格的目标会被标记为 pricing_missing，不能进入候选。无 Run 的兼容请求仍可使用它，并按现有规则处理未知费用。

## 6. 确定性决策引擎

决策输入是显式快照：

~~~text
能力契约
+ Run Ledger 快照
+ 固定模型目录、价格和策略版本
+ 全局安全覆盖版本
+ 目标健康和延迟快照
+ 当前 Run 的历史尝试摘要
+ 算法版本
~~~

输出包含首选目标、有序备用目标、每个候选的接受/淘汰原因和排序依据。

### 6.1 硬过滤

过滤顺序固定：

1. 目标和 Provider 是否启用。
2. 是否通过全局安全策略。
3. 是否满足所有必需能力。
4. 是否支持流式、工具或结构化输出。
5. 上下文窗口是否满足需求。
6. 数据等级是否允许发送。
7. 熔断器是否允许调用。
8. 剩余截止时间是否足以发起一次尝试。
9. 如果请求声明预计成本上限，是否能够完成预计成本检查。

淘汰结果使用稳定原因码，例如 missing_capability、context_window_too_small、data_policy_denied、circuit_open、deadline_insufficient 和 pricing_missing。

### 6.2 字典序排序

不使用不可解释的综合浮点分数，使用固定策略的字典序排序：

| 策略 | 排序优先级 |
| --- | --- |
| quality | 质量 → 健康 → 延迟 → 成本 |
| balanced | 健康 → 满足质量 → 成本 → 延迟 |
| economy | 满足能力 → 成本 → 健康 → 延迟 |
| deadline | 延迟 → 健康 → 质量 → 成本 |

字段相同时使用稳定的 target_id 排序。默认策略为 balanced，服务端可以禁止客户端覆盖策略。

预算较低、接近截止时间或当前目标连续失败时，Run 协调器只生成明确的输入，并记录 policy_changed_budget_low、policy_changed_deadline_near 或 target_deprioritized_recent_failures 等原因。

### 6.3 Fallback

首选和备用目标使用同一组硬约束生成，因此备用目标能力不低于原始契约。执行层仍保持：每目标最多一次、仅瞬时错误切换、总 Context 不重置、SSE 成功输出后不切换。

model=auto 搜索整个租户可用目录；显式逻辑模型只在其目标内筛选。显式模型不满足契约时返回 capability_mismatch，不静默换到无关模型。

## 7. 可靠性执行与 Provider

当前 OpenAI/Anthropic Provider、普通响应、SSE、超时、取消传播、熔断、错误分类和 Usage 采集继续保留并抽取到稳定边界。

最终支持 OpenAI Chat Completions、OpenAI Responses、Anthropic Messages、Gemini 和通用 OpenAI-Compatible Provider。

Provider 只处理一次协议调用和转换：不读取 Run 或预算，不决定逻辑模型映射，不访问数据库，不依赖 HTTP Handler，负责报告协议级 Usage 和结构化错误。

Gateway Executor 负责请求级总 Context、目标级超时、瞬时错误 Fallback、熔断、连接关闭、普通响应与 SSE 的有界实时转发、客户端取消传播、Attempt 记录和最终结算输入。

## 8. 存储与事务

PostgreSQL 是受治理状态的唯一强一致来源。配置目录和策略以严格校验的不可变 JSON 版本保存，Run 只引用版本号。

核心对象：

~~~text
tenants
api_keys
provider_credentials
config_versions
runs
run_requests
decisions
attempts
ledger_entries
audit_events
~~~

关键约束：

- 所有资源查询都带 tenant_id。
- 配置发布后不可修改，只能创建新版本。
- ledger_entries.request_id 唯一，结算幂等。
- 金额使用十进制定点整数。
- 数据库不保存 Prompt、Response、Tool 正文和明文凭据。

准入事务锁定 Run 行，检查租户、状态、截止时间、已结算预算、并发数、配置版本和账本异常；允许执行时创建 run_request 并增加在途计数。事务提交后才访问 Provider。

结算事务锁定 Request 和 Run，确认请求仍为 in_progress，写入 Attempt 和唯一 Ledger，累计已确认成本，减少在途计数，最后更新 Request 与 Run 状态。重复结算不会重复记账。

请求可能在 Provider 已产生费用后、Limen 写入账本前崩溃。租约恢复任务发现过期请求后标记为 abandoned，将 Run 设为 accounting_uncertain，阻止继续使用。若 Provider 支持按请求 ID 对账则自动恢复，否则管理员接受未知费用、补记保守金额或取消 Run。

Run 准入、预算累计、并发计数、配置版本、API Key 和审计记录由 PostgreSQL 强一致管理。Circuit Breaker 和延迟统计保留在实例内；每次决策记录健康快照，不把数据库放入 Provider 热路径。

## 9. API、鉴权与权限

### 9.1 数据平面

~~~text
POST /v1/chat/completions
POST /v1/responses
GET  /v1/models
~~~

models 只返回当前租户可使用的逻辑模型。SSE 正文不混入 Limen 私有事件。

### 9.2 控制平面

~~~text
POST /v1/limen/runs
GET  /v1/limen/runs/{run_id}
POST /v1/limen/runs/{run_id}/complete
POST /v1/limen/runs/{run_id}/cancel

GET  /v1/limen/decisions/{decision_id}
POST /v1/limen/decisions/dry-run
POST /v1/limen/decisions/{decision_id}/replay

GET  /v1/limen/configs
POST /v1/limen/configs
POST /v1/limen/configs/{version}/publish
~~~

成功响应增加 request_id、run_id、decision_id、config_version、provider、attempts 和 route 的安全摘要 Header。真实上游模型名称仅向具备审计权限的调用者展示。

### 9.3 API Key

使用高熵租户级 Key，例如 lmn_live_<public_prefix>_<random_secret>。数据库只保存公开前缀、HMAC-SHA-256 摘要、租户、Scope、状态和有效期；完整 Key 只在创建时显示一次。

固定 Scope：inference、runs:read、runs:write、decisions:read、configs:read、configs:write 和 admin。鉴权后生成统一 Principal，后续模块不接触原始 Key。

Provider 凭据在单机开发中可使用环境变量；多租户部署使用 AES-GCM 加密存储，主密钥来自部署环境或 Secret Manager，支持密钥版本轮换。

### 9.4 Explain、Dry Run、Replay

Explain 返回标准化契约、配置/算法版本、候选结果、原因码、执行计划、Attempt 和结算状态，不返回敏感正文。

Dry Run 执行真实决策但不访问 Provider、不增加 Run 计数、不产生费用。

Replay 使用历史决策快照和对应算法版本重算路由；可选比较新配置，返回原计划、重放计划和差异，不重新调用模型。

稳定错误码包括 invalid_capability_contract、capability_mismatch、no_eligible_target、run_not_active、run_budget_exhausted、run_concurrency_exceeded、run_accounting_uncertain、run_deadline_exceeded、insufficient_scope 和 config_version_unavailable。

## 10. 结算、可观测性与隐私

普通请求继续支持现有结算 Trailer；受治理请求的结算同时写入 Ledger 和结构化日志。请求结束前不因为结算等待而延迟已完成的 SSE。

日志使用 Go slog，记录 request_id、run_id、decision_id、逻辑模型、目标 ID、Attempt、状态、耗时、TTFB、结算状态和原因码。禁止记录 API Key、Authorization、Prompt、Response、Tool 正文、Provider 凭据和原始错误正文。

Metrics 使用有界 Label：endpoint、状态类别、逻辑模型、目标 ID、结果类别和拒绝原因；不使用 request ID、Run ID、用户 ID 或原始错误作为 Label。

Trace 包含请求、准入、决策、Attempt 和结算 Span；Telemetry 导出失败不能影响模型请求。

生产默认只允许 HTTPS，禁用跨主机重定向，Provider 地址使用允许列表，不原样透传客户端 Header，并限制请求体、Header、SSE 事件、空闲连接、Provider 错误体和在途请求数量。

livez 只表示进程存活，readyz 表示数据库、配置和接收状态正常。关闭时先变为未就绪，再停止准入，等待有限时间后取消剩余 Context，并由租约任务处理未结算请求。

## 11. 依赖与部署

核心 HTTP、日志和并发使用 Go 标准库。允许的外部依赖仅用于明确边界：成熟 PostgreSQL 驱动、OpenTelemetry SDK 和 Prometheus 兼容导出器。

不引入 Web 框架、ORM、依赖注入框架、任意脚本策略引擎或通用 Provider 插件市场。配置导入和数据库迁移使用版本化 SQL；本地开发仍可使用模型 JSON 文件作为启动引导。

单个二进制可运行于 Docker 或裸机；多个实例共享 PostgreSQL。Circuit Breaker 和延迟统计保留在实例内，每次决策记录健康快照。

## 12. 测试与验收

### 12.1 单元测试

- 契约解析、硬过滤、四种排序策略和原因码。
- 相同快照的确定性决策。
- 能力安全 Fallback 和 Run 状态转换。
- 定点金额、舍入和溢出保护。
- 配置版本、Scope、租户隔离和 Key 摘要。

### 12.2 Provider 契约测试

每个 Provider 覆盖普通响应、SSE、Tool Call、结构化输出、Usage、错误、超时、取消、非法 JSON、超大事件、响应关闭和敏感日志。

### 12.3 PostgreSQL 集成测试

使用真实 PostgreSQL 覆盖迁移、并发准入、并发结算、唯一账本、租户隔离、配置不可变、租约恢复和多实例竞争。

### 12.4 HTTP 与故障测试

覆盖 OpenAI SDK 兼容、Chat/Responses、普通/SSE、Run 全生命周期、Explain/Dry Run/Replay、Scope、Provider 429/5xx/断流、数据库暂时不可用、进程崩溃、客户端断开和 Telemetry 失败。

必须证明：SSE 首段不等待完整响应；已输出成功内容后不 Fallback；未知 Usage 不写成零；结算失败不改写已经确定的模型响应。

### 12.5 验证命令

~~~bash
gofmt -w $(rg --files -g '*.go')
go vet ./...
go test ./... -race
go clean -testcache
make check
make build
make smoke
make bench
git diff --check
~~~

## 13. 实施顺序

### 阶段 A：可解释决策核心

扩展能力目录、能力契约、model=auto、确定性 Decision Engine、能力安全 Fallback、内存版 Explain/Dry Run，并保持现有 Provider 和 SSE 回归通过。

### 阶段 B：Run 与可信账本

引入 PostgreSQL、租户和 Scope、Run 生命周期、并发准入、请求后结算、Ledger、账本不确定状态和多实例事务测试。

### 阶段 C：版本化控制面与 Replay

实现不可变配置发布、Decision Journal、Explain/Dry Run/Replay API、算法版本和管理审计。

### 阶段 D：Agent 协议能力

增加 Responses API、Tool Call 和结构化输出标准化，接入 Gemini 与通用 OpenAI-Compatible Provider，并复用 Provider 契约测试。

### 阶段 E：生产化与 1.0

完成 Telemetry、安全凭据轮换、租约恢复、网络策略、性能和故障注入、部署迁移备份文档，以及端到端演示。

每个阶段都必须独立运行检查、同步 README、AGENTS.md、docs/design.md 和变更记录，并形成可运行提交。

## 14. 明确不包含

最终版本不包括：

- 上百个 Provider 和插件市场。
- 用另一个大模型作为默认路由分类器。
- Prompt/Response 默认持久化。
- 语义缓存、向量数据库和自动知识库。
- 支付、发票和商业账单系统。
- 任意复杂工作流编排器或工具执行沙箱。
- Kubernetes Operator 和大型管理后台。
- 无法解释的机器学习路由分数。
- 在请求中途为了预算而截断响应。

Limen 管理模型决策和执行边界，不取代 Agent Framework。
