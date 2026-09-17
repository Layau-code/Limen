# Limen 最终版本设计

## 1. 定位与目标

Limen 是面向 Agent 工作流的可解释模型决策网关。

它接收 OpenAI 兼容请求，允许客户端声明本次任务需要的能力和约束；Limen 根据整个 Agent Run 的软预算、截止时间、模型能力和熔断观察状态动态选择上游模型，并为每次选择提供可审计、可回放的依据。

Limen 的核心不是支持尽可能多的 Provider，而是把一次 Agent 任务作为治理单位：

~~~text
能力契约
  → Run 级决策
  → 能力安全的可靠执行
  → 请求结束后结算
  → 决策解释与回放
~~~

最终版本需要同时满足：

- 现有 OpenAI SDK 可以通过修改 base_url 使用文档明确列出的 Chat Completions 子集；不支持的字段必须显式拒绝。
- Agent 可以跨多个模型请求共享软预算、截止时间和策略。
- Fallback 不会选择缺少必要能力的模型。
- 每次决策能回答“为什么选它、为什么跳过其他目标”。
- 相同的决策快照和算法版本可以得到相同的路由结果。
- Prompt、Response、API Key 和 Provider 凭据默认不被持久化或写入日志。
- 代码、注释、测试和文档保持简洁、可读，正文使用简体中文。

1.0 聚焦 OpenAI 与 Anthropic 的文本 Chat、普通响应和 SSE。Responses、Tools、多模态、第三个 Provider 和通用 OpenAI-Compatible 接入属于 1.0 之后的扩展，不能在协议尚未实现时声明对应能力。

## 2. 设计原则

1. 先保证协议和可靠性，再增加智能策略。Provider 只负责协议适配，路由和治理由上层完成。
2. 能力契约优先于模型名称。Agent 依赖能力和约束，不直接依赖某个厂商模型。
3. 决策必须可解释。使用有序过滤和字典序排序，不使用无法说明来源的黑盒综合分数。
4. Run 使用软预算阈值，费用只在请求结束后结算。不为了费用控制截断已开始的普通响应或 SSE；达到阈值后只阻止后续请求。
5. 未知值不伪装成零。缺少 Usage、价格或完整账本时，明确记录不完整状态。
6. 数据面实例不保存本地强一致状态。共享状态放入 PostgreSQL，实例内熔断状态只作为带快照的运行输入。
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
Run Coordinator（准入、状态、快照和持久化）
        ↓
Decision Engine（DecisionInput → ExecutionPlan）
        ↓
Gateway Executor（ExecutionPlan → ExecutionReport）
        ↓
Provider Adapter（协议转换）
        ↓
OpenAI / Anthropic
        ↓
Usage 结算、Ledger、Decision Journal
~~~

推荐的内部边界：

~~~text
internal/auth       Principal、Scope、租户隔离
internal/config     环境变量、配置导入和校验
internal/catalog    模型能力目录和不可变版本
internal/decision   纯决策、ExecutionPlan、Explain、Dry Run、Replay
internal/run        Run 生命周期、快照采集、准入、持久化和结算协调
internal/gateway    执行计划、熔断、Fallback、流式和 ExecutionReport
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

Run Coordinator 只采集状态并持久化结果，不改变路由策略。Decision Engine 不访问数据库、不访问网络、不调用 Provider；所有外部状态先形成版本化快照再传入。它输出不可变 ExecutionPlan。Gateway Executor 只消费计划并产生 ExecutionReport，不解析逻辑模型、不写 Ledger、不决定策略，也不依赖 HTTP 框架对象。Provider 不知道 Run、预算和逻辑模型映射。

## 4. 调用模型与 Run 生命周期

### 4.1 无状态兼容调用

没有 X-Limen-Run-ID 时，保留当前单请求行为：请求独立鉴权、解析逻辑模型、执行并在响应结束后结算。现有客户端无需理解 Run，也不受 PostgreSQL 账本状态影响。

### 4.2 受治理调用

客户端先创建 Run：

~~~http
POST /v1/limen/runs
Authorization: Bearer <limen-key>
Idempotency-Key: create-agent-run-001
~~~

~~~json
{
  "soft_budget_usd": "1.000000000",
  "deadline": "2026-09-17T18:00:00+08:00",
  "max_parallelism": 4,
  "strategy": "balanced"
}
~~~

聊天请求通过 X-Limen-Run-ID Header 关联。受治理请求必须同时携带 Idempotency-Key；无 Run 请求可以选择携带。

Run 创建时固定模型目录版本、策略版本和价格版本。模型实时健康状态不固定，但每次决策保存实际使用的健康快照。全局模型封禁、密钥撤销和安全策略可以覆盖 Run 的固定配置。

soft_budget_usd 是事后停用阈值，不是严格消费上限。系统不预留预计费用，也不设置单请求金额上限，因此单个请求的金额超支没有固定上界；max_parallelism 只能限制同时在途请求数量。这一语义是项目已确定的取舍，API、日志和文档不得把它描述为硬预算。

### 4.3 Run、Request 与结算状态

~~~text
active
  ├── completing ───────→ completed
  ├── suspended_accounting ─→ active / completing / 终态
  ├── cancelled
  ├── deadline_exceeded
  └── soft_budget_exhausted
~~~

只有 active 状态允许开始新请求。completing 和 suspended_accounting 是可恢复的非终态，其余状态是终态。

Run 转移规则：

- complete 命令把 active 改为 completing，停止准入；所有 Request 完成结算后转为 completed。
- cancel 命令把 Run 改为 cancelled，停止准入并尽力取消全部在途 Context。
- 截止时间到达后改为 deadline_exceeded，并尽力取消在途请求。
- 已确认累计费用达到 soft_budget_usd 后改为 soft_budget_exhausted；已开始的请求仍可完成。
- Usage、价格或持久化结果不确定时改为 suspended_accounting；对账完成后根据截止时间、软预算和 complete 标记恢复为 active、completing 或对应终态。
- 终态竞争的优先级固定为 cancelled、deadline_exceeded、soft_budget_exhausted、completed，所有触发原因仍保存在审计事件中。

单个 Request 状态：

~~~text
admitted → decision_ready → executing → settlement_pending → settled
                         ├─→ failed
                         ├─→ cancelled
                         └─→ abandoned
~~~

Attempt 在访问 Provider 前持久化为 started，执行结束后变为 succeeded、transient_failed、deterministic_failed、cancelled 或 abandoned。Gateway 只返回 AttemptReport，由 Run Coordinator 完成状态持久化。

### 4.4 幂等与并发

幂等唯一键为 tenant_id、endpoint、idempotency_key，数据库同时保存规范化请求哈希。哈希输入包括 Run ID、endpoint、规范请求体和影响语义的 Limen Header，不包括 Authorization：

- 推理请求第一次创建 run_request 后才允许访问 Provider。
- 推理请求使用相同 Key、相同哈希且仍在执行时返回 409 request_in_progress 和原 request_id。
- 推理请求使用相同 Key、相同哈希且已经结束时返回 409 request_already_processed、原 request_id、decision_id 和结算状态，不重新调用 Provider。
- 任何接口使用相同 Key、不同哈希都返回 409 idempotency_conflict。
- 创建 Run、结束/取消 Run 和发布配置使用相同 Key、相同哈希时返回原资源或原操作结果。
- 为保护隐私，Limen 不保存完整模型响应用于重放，因此推理幂等保证“不会重复调用和重复计费”，不保证重新返回原响应正文。

准入事务只检查已经结算的累计成本。多个已获准请求可以并发完成，因此最终金额可能超过软预算阈值。max_parallelism 只限制在途和待结算请求数量；Request 只有进入结算终态后才释放名额。Run 行锁只覆盖准入或结算事务，不覆盖网络调用。

### 4.5 跨实例取消

取消操作先以事务更新 Run 并写入 cancellation_event，再通过 PostgreSQL LISTEN/NOTIFY 通知实例。当前实现已增加独立取消监听器和进程内广播器；凭据变更也使用独立监听器刷新 Provider，数据库记录仍是唯一事实来源。取消轮询继续作为断线兜底。

目标是在正常数据库连接下 p95 两秒内把跨实例取消传播到 Provider。即使通知丢失，轮询和截止时间也必须最终停止请求。

单个受治理 Run 的流程：

~~~text
鉴权
  → 锁定 Run 并检查准入
  → 校验 Idempotency-Key 和请求哈希
  → 解析能力契约和固定配置
  → 获取健康快照
  → 生成并持久化 DecisionInput / ExecutionPlan
  → 调用前持久化 Attempt started
  → Executor 执行计划并产生 ExecutionReport
  → 实时发送响应
  → 响应结束后进入 settlement_pending
  → 更新 Ledger、Request 和 Run 状态
~~~

## 5. 能力契约与模型目录

### 5.1 请求扩展

受支持 Chat Completions 子集的普通请求仍可指定逻辑模型：

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
    "required_capabilities": ["text"],
    "minimum_quality_tier": 3,
    "required_context_tokens": 32000,
    "estimated_input_tokens": 4000,
    "estimated_output_tokens": 1200,
    "data_class": "internal",
    "strategy": "balanced"
  }
}
~~~

limen 通过 OpenAI SDK 的 extra_body 传入。Run ID 使用 Header，避免私有字段被转发给 Provider。

字段语义固定如下：

| 字段 | 类型 | 规则 |
| --- | --- | --- |
| required_capabilities | 硬约束 | 目标必须包含全部能力 |
| minimum_quality_tier | 硬约束 | 目标等级必须大于等于该值 |
| required_context_tokens | 硬约束 | 目标上下文窗口必须足够 |
| data_class | 硬约束 | 目标必须允许该数据等级 |
| estimated_input_tokens / estimated_output_tokens | Hint | 只用于预计费用，不参与实际结算 |
| strategy | Preference | 决定合格目标的稳定排序方式 |

stream 来自标准请求字段，并作为硬约束检查目标的 supports_streaming。没有 Token 估算时不猜测 Tokenizer，只使用 cost_tier 排序。Limen 不提供单请求费用上限，Token 估算不能阻止已经获准的请求。

全局安全策略优先于租户配置，租户配置优先于 Run。Run 创建后固定 strategy，请求内不得覆盖；无 Run 请求可以从租户允许列表中选择。冲突返回 strategy_conflict，不静默忽略。

data_class 使用固定等级 public、internal、confidential、restricted；目标显式列出允许等级，不通过字符串大小关系推断。

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
      "capabilities": ["text"],
      "supports_streaming": true,
      "quality_tier": 3,
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

1.0 能力目录只允许当前端到端协议真实支持的 text，并单独声明 supports_streaming。context_window 使用数值约束，不再重复声明 long_context。vision、tool_calling 和 structured_output 只有在请求转换、响应转换和契约测试完成后才能加入允许枚举。

对于受治理 Run，缺少完整价格的目标会被标记为 pricing_missing，不能进入候选。无 Run 的兼容请求仍可使用它，并按现有规则处理未知费用。

## 6. 确定性决策引擎

决策输入是显式快照：

~~~text
能力契约
+ Run Ledger 快照
+ 固定模型目录、价格和策略版本
+ 全局安全覆盖版本
+ 目标健康与熔断观察快照
+ 显式评估时间
+ 算法版本
~~~

输出包含首选目标、有序备用目标、每个候选的接受/淘汰原因和排序依据。

DecisionInput 使用版本化、强类型 Schema，而不是 map[string]any：

~~~json
{
  "schema_version": "decision-input.v1",
  "algorithm_version": "decision.v1",
  "evaluated_at_unix_ms": 1789640000000,
  "tenant_id": "tenant_01",
  "run": {},
  "contract": {},
  "catalog": [],
  "security_overrides": [],
  "health": []
}
~~~

所有时间、金额和比例使用整数或十进制定点字符串，候选和原因码使用规定顺序。持久化派生结果前先保存完整原始 DecisionInput。规范 JSON 使用固定字段、UTF-8、无多余空白、对象键排序和稳定数组顺序，并计算 SHA-256 input_hash；ExecutionPlan 使用同样规则计算 plan_hash。

仓库提交 golden fixture，证明进程重启后同一 DecisionInput 产生字节级相同的规范 ExecutionPlan。历史 Decision 保留期间必须保留对应算法实现，最低支持十二个月；实现不可用时 Replay 返回 algorithm_version_unavailable，但 Explain 仍可读取原始记录。Replay 只复现决策计划，不声称复现 Provider 执行结果。

### 6.1 硬过滤

过滤顺序固定：

1. 目标和 Provider 是否启用。
2. 是否通过全局安全策略。
3. 是否满足所有必需能力。
4. 质量等级是否达到 `minimum_quality_tier`。
5. 是否支持请求要求的流式模式。
6. 上下文窗口是否满足需求。
7. 数据等级是否允许发送。
8. 熔断器是否允许调用。
9. 剩余截止时间是否大于策略配置的 minimum_attempt_window，默认二百五十毫秒。

淘汰结果使用稳定原因码，例如 missing_capability、quality_tier_too_low、context_window_too_small、data_policy_denied、circuit_open、deadline_insufficient 和 pricing_missing。

### 6.2 字典序排序

1.0 不使用不可解释的综合浮点分数，只保留两种固定策略：

| 策略 | 排序优先级 |
| --- | --- |
| balanced | 熔断观察状态 → 质量 → 成本等级 → target_id |
| economy | 成本等级 → 熔断观察状态 → 质量 → target_id |

minimum_quality_tier 已经在硬过滤中处理，排序中的质量只比较合格目标。默认策略为 balanced。动态延迟统计、历史失败降权、quality 和 deadline 策略在拥有真实指标和测试数据后再设计。

成本排序在两个 Token 估算都存在且目标有完整价格时使用定点预计费用，否则使用 cost_tier。预计费用只用于排序，实际结算只信任 Provider Usage。

Run Coordinator 只采集并传入 Run、目录、安全和健康快照。任何候选过滤、策略选择和原因码都由 Decision Engine 产生，不能在 Run 层隐藏修改。

Run 策略可以配置 economy_threshold_percent，默认 20。balanced Run 的剩余已结算软预算比例低于该阈值时，Decision Engine 把本次 effective_strategy 确定为 economy，并记录 economy_threshold_reached；Run Coordinator 只提供原始累计金额和阈值。该切换发生在硬过滤之后，不会牺牲能力、质量下限或数据等级。

### 6.3 Fallback

首选和备用目标使用同一组硬约束生成，因此备用目标能力不低于原始契约。执行层仍保持：每目标最多一次、仅瞬时错误切换、总 Context 不重置、SSE 成功输出后不切换。

model=auto 搜索整个租户可用目录；显式逻辑模型只在其目标内筛选。显式模型不满足契约时返回 capability_mismatch，不静默换到无关模型。

熔断快照只表示 Decision 时的 observed_eligible。Half-Open 探测资格在 Executor 调用前原子占用；如果计划生成后资格被其他请求占用，Executor 记录 skipped_due_to_race 并继续下一个计划目标。Replay 重现原计划，ExecutionReport 负责说明运行时竞态。

Provider Adapter 把厂商状态和传输错误归一为 `retryable_transient`、`deterministic_request`、`authentication`、`quota`、`cancelled` 和 `internal`。当前 OpenAI/Anthropic 适配器已把上游 HTTP 状态归一化到该边界；只有 `retryable_transient` 允许执行计划中的下一个目标，具体分类由 Provider 契约测试固定。

## 7. 可靠性执行与 Provider

当前 OpenAI/Anthropic Provider、普通响应、SSE、超时、取消传播、熔断、错误分类和 Usage 采集继续保留并抽取到稳定边界。

1.0 只承诺以下 OpenAI Chat Completions 子集：

| 字段/行为 | 1.0 |
| --- | --- |
| model | 支持逻辑模型和 auto |
| messages | 支持 system、user、assistant 的纯文本 |
| max_tokens | 支持 |
| temperature | 支持 |
| stream | 支持普通响应和 SSE |
| tools / tool_choice | 不支持，返回 unsupported_field |
| response_format | 不支持，返回 unsupported_field |
| 图片、音频和多模态内容 | 不支持，返回 unsupported_field |
| n、logprobs 等其他字段 | 未列入矩阵即拒绝，不能静默丢弃 |

兼容测试使用真实 OpenAI SDK 构造请求，但不访问真实 Provider 网络。README 必须链接这份字段矩阵，不能笼统宣称完整 OpenAI 兼容。

Provider 只处理一次协议调用和转换：不读取 Run 或软预算，不决定逻辑模型映射，不访问数据库，不依赖 HTTP Handler，负责报告协议级 Usage、上游 request ID 和归一化错误。

核心接口语义：

~~~text
DecisionEngine.Decide(DecisionInput) → ExecutionPlan
GatewayExecutor.Execute(Context, ExecutionPlan, ChatRequest) → ExecutionReport
~~~

Gateway Executor 负责请求级总 Context、目标级超时、瞬时错误 Fallback、熔断、连接关闭、普通响应与 SSE 的有界实时转发、客户端取消传播，并按真实调用顺序产生 AttemptReport。调用前由 Run Coordinator 通过 AttemptStartHook 持久化对应 Attempt，Executor 不持久化 Attempt 或 Ledger；Run Coordinator 根据 ExecutionReport 完成事务。

Responses、Tools、结构化输出、多模态、Gemini 和通用 OpenAI-Compatible Provider 属于 1.0 后续演进。届时根据至少两个真实协议提取请求模型，禁止使用 map[string]any 作为万能请求结构。

## 8. 存储与事务

PostgreSQL 是受治理状态的唯一强一致来源。配置目录和策略以严格校验的不可变 JSON 版本保存，Run 只引用版本号。阶段 B 即使仍从只读文件加载配置，也必须以规范内容 SHA-256 作为不可变 config_version；阶段 C 只把发布入口迁移到数据库，不改变版本语义。

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
settlement_jobs
cancellation_events
audit_events
~~~

关键约束：

- 每张租户表都保存 tenant_id，并使用 tenant_id 与资源 ID 的组合主键或唯一键。
- 跨表引用使用包含 tenant_id 的组合外键，阻止错误租户绑定。
- 关键租户表启用并强制 PostgreSQL RLS；普通连接在事务内使用 SET LOCAL 设置 tenant_id，系统管理使用独立数据库角色。
- 配置发布后不可修改，只能创建新版本。
- ledger_entries.request_id 唯一，结算幂等。
- attempts 使用本地 attempt_id 唯一，上游 request ID 在获得后补写。
- 金额使用十进制定点整数。
- 数据库不保存 Prompt、Response、Tool 正文和明文凭据。

### 8.1 准入与调用前持久化

准入事务锁定 Run 行，检查租户、状态、截止时间、已结算软预算、并发数、配置版本、幂等键和账本异常；允许执行时创建 run_request、增加在途计数并取得租约。事务提交后才生成决策。

DecisionInput 和 ExecutionPlan 持久化后，每次真实网络调用都先插入 attempt started，再访问 Provider。Attempt 保存本地 attempt_id、目标、开始时间、租约持有实例和可选的上游幂等标识；收到响应 Header 后补写 provider_request_id。没有 Provider 查询能力时，该记录仍能证明一次调用可能已经发生。

### 8.2 结算和恢复

上游结束后 Request 进入 settlement_pending。结算事务锁定 Request 和 Run，确认状态仍可结算，写入 AttemptReport 和唯一 Ledger，累计已确认成本，减少在途计数，最后把 Request 改为 settled，并按状态机更新 Run。重复执行不会重复记账。

正常路径为同步结算，但只能在 SSE 最后一段数据已经 Flush 后等待，最多阻塞 HTTP EOF 五百毫秒：

- 事务在期限内成功：Trailer 和查询 API 返回 complete、partial 或 unavailable。
- 数据库暂时失败：Trailer 返回 pending，HTTP 结束；当前实现先在进程内按 0、100、500 毫秒退避重试，仍失败时写入 `settlement_jobs`，由持久化后台任务继续处理。
- 重试仍失败或实例退出：数据库中的 executing/settlement_pending 租约过期，由其他实例抢占恢复。
- Usage、价格或 Provider 对账仍不能确定：Request 变为 abandoned，Run 变为 suspended_accounting。

执行实例每十秒续租，租约三十秒过期。数据库恢复后，待结算请求必须在三十秒内被本地重试或租约扫描重新处理。当前实现已具备租约扫描、短退避重试和持久化后台结算任务；多实例故障注入测试仍需补齐。租约恢复、后台结算和幂等状态转换必须与 Run 同在阶段 B 交付，不能后置。

如果 Provider 支持按 request ID 查询 Usage，则自动对账；否则管理员只能接受未知费用、补记保守金额或取消 Run。处理结果写入新的审计事件，不能覆盖原 Attempt。

### 8.3 强一致范围

Run 准入、软预算累计、并发计数、配置版本、API Key 和审计记录由 PostgreSQL 强一致管理。Circuit Breaker 保留在实例内；每次决策记录熔断观察快照，不把数据库放入 Provider 热路径。

## 9. API、鉴权与权限

### 9.1 数据平面

~~~text
POST /v1/chat/completions
GET  /v1/models
~~~

models 只返回当前租户可使用的逻辑模型。SSE 正文不混入 Limen 私有事件。

### 9.2 控制平面

~~~text
POST /v1/limen/runs
GET  /v1/limen/runs/{run_id}
GET  /v1/limen/runs/{run_id}/requests/{request_id}
POST /v1/limen/runs/{run_id}/complete
POST /v1/limen/runs/{run_id}/cancel

GET  /v1/limen/decisions/{decision_id}
POST /v1/limen/decisions/dry-run
POST /v1/limen/decisions/{decision_id}/replay

GET  /v1/limen/configs
GET  /v1/limen/configs/{version}/diff/{base_version}
POST /v1/limen/configs
POST /v1/limen/configs/{version}/publish

POST /v1/limen/credentials/{provider}
POST /v1/limen/credentials/{provider}/revoke
~~~

成功响应增加 request_id、run_id、decision_id、config_version、provider、attempts 和 route 的安全摘要 Header。真实上游模型名称仅向具备审计权限的调用者展示。

创建 Run 返回：

~~~json
{
  "id": "run_01...",
  "state": "active",
  "soft_budget_usd": "1",
  "settled_cost_usd": "0",
  "config_version": "sha256:...",
  "strategy": "balanced"
}
~~~

Request 查询返回执行状态、decision_id 和结算状态，不返回 Prompt 或 Response。settlement_status 为 pending 时，客户端通过该接口观察最终结果，不能依赖 HTTP Trailer 作为账本事实来源。

### 9.3 API Key

使用高熵租户级 Key，例如 lmn_live_<public_prefix>_<random_secret>。数据库只保存公开前缀、HMAC-SHA-256 摘要、租户、Scope、状态和有效期；完整 Key 只在创建时显示一次。

固定 Scope：inference、runs:read、runs:write、decisions:read、configs:read、configs:write 和 admin。鉴权后生成统一 Principal，后续模块不接触原始 Key。

当前实现同时支持静态和 PostgreSQL API Key Store：静态模式使用 `LIMEN_API_KEY`、`LIMEN_TENANT_ID` 和 `LIMEN_API_SCOPES`；PostgreSQL 模式按公开前缀查询 HMAC-SHA-256 摘要、租户和 Scope，成功后生成统一 Principal。HTTP 层在入口校验接口所需 Scope，并把 Principal 租户传入 Run 哈希、准入和结算路径；Key 创建、轮换和管理 API 仍待后续控制面阶段实现。Provider 凭据可通过 `LIMEN_CREDENTIAL_MASTER_KEY` 启用 AES-GCM 加密存储，密文附加认证数据绑定 tenant、Provider 和 endpoint，Provider 适配器支持原子替换密钥。启用凭据存储后，`admin` 可调用凭据轮换和撤销 API；接口只接受配置绑定的 endpoint_id，响应不返回明文密钥。

| 接口 | 所需 Scope |
| --- | --- |
| Chat Completions、Models | inference |
| 创建、结束、取消 Run | runs:write |
| 查询 Run 和 Request | runs:read |
| Explain、Replay | decisions:read |
| Dry Run | inference 与 decisions:read |
| 读取配置 | configs:read |
| 比较配置版本 | configs:read |
| 创建和发布配置 | configs:write |
| 轮换和撤销 Provider 凭据 | admin |

Provider 凭据在单机开发中可使用环境变量；多租户部署从阶段 B 起使用 AES-GCM 加密存储，主密钥来自部署环境或 Secret Manager。每份凭据绑定 tenant_id、provider 和经过校验的 endpoint_id，不能只按 Provider 名称复用。当前已提供管理员轮换和撤销接口；轮换立即更新当前实例，跨实例变更通知和 Secret Manager 仍后置。

### 9.4 Explain、Dry Run、Replay

Explain 返回标准化契约、配置/算法版本、候选结果、原因码、执行计划、Attempt 和结算状态，不返回敏感正文。

Dry Run 执行真实决策但不访问 Provider、不增加 Run 计数、不产生费用。

Replay 校验 input_hash 后，使用历史 DecisionInput 和对应算法版本重新生成规范 ExecutionPlan，并比较 plan_hash；可选比较新配置，返回原计划、重放计划和结构化差异，不重新调用模型或复现运行时 Attempt。

当前基础实现已持久化 DecisionInput/ExecutionPlan、`input_hash`、`plan_hash` 和算法版本，并通过算法注册表执行 Explain/Replay；配置版本控制面已提供创建、列表、结构化 diff 和发布 API，发布会原子替换 Router 目录与路由参数。旧算法实现保留窗口、审批审计和完整差异树仍待补齐。

稳定错误码包括 invalid_capability_contract、unsupported_field、strategy_conflict、capability_mismatch、no_eligible_target、run_not_active、run_soft_budget_exhausted、run_concurrency_exceeded、run_accounting_suspended、run_deadline_exceeded、request_in_progress、request_already_processed、idempotency_conflict、insufficient_scope、config_version_unavailable 和 algorithm_version_unavailable。

| HTTP | 错误码 |
| ---: | --- |
| 400 | invalid_capability_contract、unsupported_field、strategy_conflict、capability_mismatch |
| 403 | insufficient_scope |
| 408 | run_deadline_exceeded |
| 409 | run_not_active、run_accounting_suspended、request_in_progress、request_already_processed、idempotency_conflict、config_version_unavailable、algorithm_version_unavailable |
| 429 | run_soft_budget_exhausted、run_concurrency_exceeded |
| 503 | no_eligible_target |

错误继续使用 OpenAI 风格 envelope，并在 code 中保留以上稳定值。错误正文不能包含上游 URL、凭据、原始 Provider 响应或跨租户资源是否存在的信息。

## 10. 结算、可观测性与隐私

普通无 Run 请求继续支持现有内存结算 Trailer。受治理请求以 PostgreSQL Ledger 和 Request 查询接口为事实来源：

- SSE 文本和最终 DONE 事件到达后立即 Flush，不能等待数据库。
- 上游 EOF 后开始有界同步结算，最多延迟 HTTP EOF 五百毫秒。
- 成功时 Trailer 可以返回最终状态；超时或失败时只返回 pending，由后台恢复。
- 客户端不能把 Trailer 当成最终账本，也不能假设收到 DONE 时结算已经完成。
- Request 在结算终态前继续占用 Run 并发名额；不确定时暂停 Run 后续准入。

日志使用 Go slog，记录 request_id、run_id、decision_id、逻辑模型、目标 ID、Attempt、状态、耗时、TTFB、结算状态和原因码。禁止记录 API Key、Authorization、Prompt、Response、Tool 正文、Provider 凭据和原始错误正文。基础 `/metrics` 仅输出固定计数器和有界标签，并要求 `admin` Scope。

Metrics 使用有界 Label：endpoint、状态类别、逻辑模型、目标 ID、结果类别和拒绝原因；不使用 request ID、Run ID、用户 ID 或原始错误作为 Label。

Trace 包含请求、准入、决策、Attempt 和结算 Span；Telemetry 导出失败不能影响模型请求。

持久化使用字段白名单而不是对请求对象做通用序列化。DecisionInput 只保存契约元数据、目录、版本、健康状态和定点数值；messages、工具正文、原始 Header 和 Provider 错误正文在类型层面不属于可持久化 Schema。

多租户出站网络使用专用安全 HTTP Client：

- 生产只允许 HTTPS；HTTP 只能在显式开发模式使用。
- 默认拒绝所有重定向，不能把 Authorization 或 x-api-key 带到新地址。
- endpoint_id 在发布配置时绑定租户、Provider、域名、端口和路径前缀。
- 域名必须在租户和部署级允许列表中。
- 自定义 DialContext 在每次连接时解析域名，并拒绝环回、私网、链路本地、组播、未指定地址和云元数据地址；实际连接只能使用已检查的 IP，防止 DNS 重绑定。
- 默认禁用环境 HTTP_PROXY 和 HTTPS_PROXY；需要代理时只能使用部署级显式配置。
- 不原样透传客户端 Header，不使用用户输入拼接 Host，也不允许凭据跨 endpoint_id 复用。
- 限制请求体、Header、SSE 事件、空闲连接、Provider 错误体和在途请求数量。

livez 只表示进程存活，readyz 表示数据库、配置和接收状态正常。关闭时先变为未就绪，再停止准入，等待有限时间后取消剩余 Context，并由租约任务处理未结算请求。

## 11. 依赖与部署

核心 HTTP、日志和并发使用 Go 标准库。允许的外部依赖仅用于明确边界：成熟 PostgreSQL 驱动、OpenTelemetry SDK 和 Prometheus 兼容导出器。

不引入 Web 框架、ORM、依赖注入框架、任意脚本策略引擎或通用 Provider 插件市场。配置导入和数据库迁移使用版本化 SQL；本地开发仍可使用模型 JSON 文件作为启动引导。

单个二进制可运行于 Docker 或裸机；多个实例共享 PostgreSQL。Circuit Breaker 保留在实例内，每次决策记录熔断观察快照。

## 12. 测试与验收

### 12.1 单元测试

- 契约解析、硬过滤、两种排序策略和原因码。
- 相同快照的确定性决策。
- 能力安全 Fallback 和 Run 状态转换。
- 定点金额、舍入和溢出保护。
- 配置版本、Scope、租户隔离和 Key 摘要。
- DecisionInput 规范 JSON、input_hash、plan_hash 和 golden fixture。

### 12.2 Provider 契约测试

每个 1.0 Provider 覆盖纯文本普通响应、SSE、Usage、归一化错误、超时、取消、非法 JSON、超大事件、响应关闭和敏感日志。不支持的 Tools、结构化输出和多模态请求必须显式拒绝。

### 12.3 PostgreSQL 集成测试

使用真实 PostgreSQL 覆盖迁移、并发准入、幂等键、并发结算、唯一账本、组合外键、RLS、配置不可变、后台重试、租约抢占、跨实例取消和多实例竞争。

### 12.4 HTTP 与故障测试

覆盖 OpenAI SDK 兼容矩阵、Chat 普通/SSE、Run 全生命周期、Explain/Dry Run/Replay、Scope、Provider 429/5xx/断流、数据库暂时不可用、进程崩溃、客户端断开、重定向/DNS 重绑定和 Telemetry 失败。

必须证明：SSE 首段不等待完整响应；已输出成功内容后不 Fallback；未知 Usage 不写成零；结算失败不改写已经确定的模型响应。

### 12.5 量化验收

- 对同一 Run 发起 100 个并发准入请求，max_parallelism=8 时任意时刻最多 8 个处于在途或待结算状态，最终计数无丢失。
- 对同一 Idempotency-Key 并发请求 100 次，Provider 实际调用至多一次，Ledger 至多一条；不同请求体稳定返回 idempotency_conflict。
- 至少 100 组已提交 DecisionInput fixture 在重启后得到字节级相同的规范 ExecutionPlan 和 plan_hash。
- 数据库恢复后，settlement_pending 请求在三十秒内完成重试或转为 suspended_accounting，不永久占用并发名额。
- 跨实例取消在正常数据库连接下 p95 不超过两秒，丢失 NOTIFY 后轮询仍能生效。
- 本地可控 Provider 在发出首块后，网关 p95 在一百毫秒内 Flush；完整响应未结束时客户端已经读到首块。
- 100 个候选目标的纯决策基准 p95 小于五毫秒，不包含数据库和 Provider 时间。
- 日志、Trace、Metrics、Decision 和数据库导出中，API Key、Provider Key、Prompt、Response 与 Tool 哨兵字符串出现次数为零。
- 环回、RFC1918、链路本地、云元数据地址、跨主机重定向、DNS 重绑定和环境代理绕过测试全部被拒绝。
- 软预算测试必须证明当前请求不会因超额被截断，同时明确其金额超支没有硬上界。

### 12.6 验证命令

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

先把现有 Router 拆成 Decision Engine 和 Gateway Executor；增加稳定 target_id、只包含 text/streaming 的能力目录、最小能力契约、model=auto、balanced/economy 排序、ExecutionPlan/ExecutionReport、配置内容哈希、DecisionInput 规范序列化、内存 Explain/Dry Run 和 golden replay 测试。同步实现专用安全 HTTP Client，并保持现有 Chat、SSE、熔断、Usage 和安全日志全部回归通过。

### 阶段 B：Run 与可信账本

引入 PostgreSQL、迁移、Tenant、Scope、组合外键与 RLS、Run/Request/Attempt 状态机、Idempotency-Key、调用前 Attempt 持久化、软预算、并发准入、同步/后台结算、Ledger、三十秒租约恢复和跨实例取消事件；当前实现已完成租约获取、续租、释放、未知费用恢复、轮询与 LISTEN/NOTIFY 取消、Provider 凭据加密与 endpoint 绑定，并为凭据变更增加 LISTEN/NOTIFY 刷新。多实例事务测试仍待补齐，阶段 B 结束时不能存在崩溃后永久占用的并发名额。

### 阶段 C：版本化控制面与 Replay

把文件内容哈希升级为不可变配置发布流程；实现持久化 Decision Journal、Explain/Dry Run/Replay API、配置版本创建/发布、结构化配置 diff、input_hash/plan_hash、Request 结算查询、管理审计和旧算法不可用语义。当前实现已完成配置版本基础控制面、当前算法注册和结构化路径 diff，旧算法保留窗口与审批审计仍待完成。

### 阶段 D：生产化与 1.0

完成 OpenTelemetry/Exporter、完整 Prometheus 指标、Secret Manager 接入、网络安全测试、故障注入、量化性能验收、部署迁移备份文档，以及端到端演示。当前实现已具备基础 Prometheus 文本指标、Provider 凭据轮换/撤销控制面和跨实例凭据通知边界。1.0 仍只承诺 OpenAI/Anthropic 文本 Chat 子集。

### 1.0 之后

根据真实使用需求依次评估 Responses API、Tools、结构化输出、多模态、Gemini 或通用 OpenAI-Compatible Provider。每种能力必须先完成端到端协议和契约测试，再允许在能力目录中声明；第三 Provider 和通用兼容层不能同时为了展示而引入。

每个阶段都必须独立运行检查、同步 README、AGENTS.md、docs/design.md 和变更记录，并形成可运行提交。

## 14. 1.0 明确不包含

1.0 不包括：

- 上百个 Provider 和插件市场。
- Responses API、Tools、结构化输出和多模态。
- Gemini、第三个 Provider 和通用 OpenAI-Compatible 层。
- 用另一个大模型作为默认路由分类器。
- Prompt/Response 默认持久化。
- 语义缓存、向量数据库和自动知识库。
- 严格费用预留和硬消费上限；Run 使用明确命名的软预算。
- 按用户的每日或月度额度。
- 动态延迟排名和历史失败权重。
- 支付、发票和商业账单系统。
- 任意复杂工作流编排器或工具执行沙箱。
- Kubernetes Operator 和大型管理后台。
- 无法解释的机器学习路由分数。
- 在请求中途为了预算而截断响应。

Limen 管理模型决策和执行边界，不取代 Agent Framework。
