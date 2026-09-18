# Limen 开发协作规范

## 项目定位

Limen 是面向 Agent 的 Go AI Gateway：以 OpenAI 兼容 API 接收请求，用逻辑模型注册表选择 OpenAI 或 Anthropic，并在预算内安全执行有序 Fallback。项目强调可解释、可测试和小而清晰的实现，不为展示技术栈增加复杂度。

## 当前交付范围

- `POST /v1/chat/completions`、`GET /v1/models`、`/livez`、`/readyz`。
- OpenAI 与 Anthropic 协议适配，普通响应和 SSE 流式响应。
- 启动时严格加载的模型注册表；未配置文件时保留四种前缀兼容模式，配置 API 发布后可原子替换当前进程目录。
- `GET/POST /v1/limen/configs`、配置 diff 和 `POST /v1/limen/configs/{version}/publish` 提供租户隔离的不可变配置版本控制；PostgreSQL 模式下重启恢复已发布版本，发布通过只含租户和版本哈希的通知加速跨实例刷新，并保留轮询兜底。
- 能力目录与版本化 Decision Engine：`model=auto` 或 Limen 契约会生成带输入/计划哈希的 ExecutionPlan；Replay 必须通过算法注册表解析版本，不得静默回退。
- 决策哈希必须按算法版本解释；`decision.v2` 规范化无序集合但保留显式模型的目标优先级，修改 V1 语义前必须新增版本并保留旧版本 Replay。
- `POST /v1/limen/decisions/dry-run` 只生成计划，不访问 Provider；模型文件启动或配置版本发布时生成稳定 `config_version`，后续 Run 固定引用该版本。
- `POST /v1/limen/configs/{version}/dry-run` 使用租户隔离的指定草稿生成只读计划；不得切换当前 Router、访问 Provider 或改变熔断状态。
- 配置 dry-run/replay 必须复用发布阶段的 endpoint 绑定校验，不能生成一个实际无法激活的配置计划。
- `POST /v1/limen/configs/{version}/replay` 使用租户隔离的历史 DecisionInput 对指定草稿做影响分析；不得访问 Provider、读取当前熔断状态或改变线上 Router，只返回原计划、草稿计划和安全差异。
- `internal/journal` 保存 DecisionInput/ExecutionPlan 审计快照；Dry Run、Chat、Explain 和 Replay 不得持久化 Prompt、Response 或 Provider Key。
- `internal/audit` 只保存控制面非敏感 actor_id、动作、资源摘要、请求哈希和时间；`GET /v1/limen/audit` 需要 `admin` Scope，审计故障不回滚已提交的业务变更，事件必须保持租户隔离。actor_id 只能是静态标识或 Key 公开前缀，不能是原始凭据。
- 阶段 B 的 `internal/run` 和 `internal/store` 已定义 Run/Request/Attempt、幂等和账本边界；无 Run Chat 仍走原有内存结算路径。
- HTTP Run 控制面只有在显式 `LIMEN_RUN_STORE=memory` 时启用；内存实现仅用于开发演示，不能作为生产账本。
- 配置 `LIMEN_DATABASE_URL` 后必须通过 `database/sql` 和参数化 PostgreSQL Repository 启动；迁移只使用版本表执行一次，DSN 不得进入日志。
- PostgreSQL 启动迁移必须在同一事务内先获取固定 advisory lock，再检查版本并执行 DDL；多实例并发启动不能依赖应用层互斥或偶然的 DDL 顺序。
- 主服务必须先成功绑定 `LIMEN_ADDR` 的 TCP socket，再将 Health 标记为 ready；监听失败不得短暂暴露就绪状态，服务使用已绑定的 Listener 调用 `Serve`。
- 配置发布可选启用 `LIMEN_CONFIG_APPROVAL_REQUIRED=true`；`internal/approval` 负责双人审批状态机，审批绑定租户、配置版本、发布幂等键和请求哈希，PostgreSQL 发布必须在同一事务内消费审批；静态 actor 不能满足身份分离。
- 鉴权必须先生成带 `tenant_id` 和 Scope 的 Principal；Provider、Router 和 Store 不得读取原始 API Key。静态 Key 由 `LIMEN_API_SCOPES` 限制，也可切换 PostgreSQL Key Store。HTTP 入口必须拒绝不等于 `LIMEN_TENANT_ID` 的 Principal；单个进程只服务一个租户，多租户部署使用多个实例。
- PostgreSQL Key Store 只按公开前缀查询 HMAC 摘要，使用常量时间比较校验完整 Key；数据库、日志和 Principal 均不得保存或暴露完整 Key。
- `LIMEN_API_KEY_FILE`、`OPENAI_API_KEY_FILE` 和 `ANTHROPIC_API_KEY_FILE` 只读一次启动时的文件密钥；对应明文变量与 `_FILE` 冲突必须拒绝，空文件或读取失败不能静默回退，日志不得输出密钥内容。
- PostgreSQL API Key 控制面只允许 `admin` 创建、列出、原子轮换和撤销 Key；创建与轮换必须带幂等键，明文只在首次成功响应返回，重试和列表只能返回公开前缀与 Scope。轮换必须在同一事务内创建新摘要并停用旧 Key。认证查询必须走受控数据库函数，管理查询必须设置租户上下文并通过 RLS。
- `internal/credentialstore` 使用 AES-GCM 保存 Provider 凭据密文，附加认证数据绑定租户、Provider 和 endpoint；Provider 支持并发安全的密钥替换。启用数据库和主密钥后，Chat 必须把已通过实例租户边界的 Principal `tenant_id` 传入 Provider，Provider 每次出站按该租户解析凭据，缺失凭据不得回退到其他租户或进程共享密钥。
- 模型目标可选声明 `endpoint_id`，必须匹配进程配置的 Provider endpoint ID；Router 发布前和 Provider 出站前都要拒绝错绑，熔断键也必须包含 endpoint 绑定。当前每个 Provider 只支持一个进程级 endpoint，不允许客户端传入地址或选择 endpoint。
- Chat API 当前只承诺文本消息（`system`、`developer`、`user`、`assistant`）、普通/SSE、`model`、`max_tokens`、`max_completion_tokens`、`temperature`、`stream` 和 `stream_options.include_usage`；两个输出上限字段互斥。Tools、tool calls、Vision、多模态、Responses API 与未知字段必须明确返回 `400`。
- 共享请求预算、单次尝试超时、按目标熔断、瞬时故障 Fallback、路由摘要（包括 `X-Limen-Plan-Hash`）和安全日志。
- 受治理 Request 必须在准入后取得租约，默认 30 秒过期、每 10 秒续租；租约丢失时取消本地 Context，恢复任务只能进入未知费用/暂停账本，不得盲目重放 Provider。结算存储失败时必须保留原始成本快照，写入持久化 `settlement_jobs`，由带租约的后台任务幂等恢复；已知费用不得在延迟清理中降级为未知，且请求已被租约恢复为 `abandoned` 后，处理已知费用不能再次减少 `in_flight`，必须恢复 Run 的可准入状态。没有创建任何 Provider Attempt 的请求必须按零成本结算，不能误进入 `suspended_accounting`。
- 每次真实 Provider 调用前必须写入独立 Attempt；上游返回的非敏感 request ID 可在响应后补写，不能记录 Prompt、Response 或凭据。
- Run 取消必须在状态变更事务内写入租户隔离取消事件；PostgreSQL 用 `LISTEN/NOTIFY` 加速广播，执行中的 Chat 仍通过事件轮询兜底，不能只修改当前进程的内存映射。
- 未知费用会暂停 Run；管理员可通过带 `admin` Scope 和幂等键的会计处置接口补记金额或明确接受未知费用。处置必须是事务化、可重复执行且不把未知值写成零。
- Provider 用量采集、按目标定点价格计算成本，以及响应结束后的结算 Trailer 和结构化日志。
- `/metrics` 只接受 `admin` Scope，使用固定指标名和有界标签；模型必须来自可信目录或固定归并值，Attempt 只统计真实 Provider 调用，不允许请求 ID、租户 ID、原始错误或正文进入指标。
- 延迟指标只能使用固定桶和有限基数；请求耗时、TTFB 只记录非负有限值，不能把动态路径、正文或标识放入标签。
- `/metrics` 的固定指标必须输出稳定的 `HELP`/`TYPE` 元数据，即使当前没有样本；新增指标必须同步帮助文本、类型和契约测试。
- OpenTelemetry Trace 只使用字段白名单串联请求、准入、决策、Attempt 和结算；只传播 `traceparent`，导出失败不得改变模型请求。
- `statusRecorder` 只在首次写出响应正文时记录 TTFB；日志和 Trace 可记录 `ttfb_ms`，不得把正文、Header 或密钥写入观测字段。
- `internal/decision/testdata/fixtures.json` 是版本化 Replay 证据；修改决策语义必须先更新生成器和算法版本，`make check` 必须证明生成结果无漂移。
- Decision Journal 的 HTTP 响应必须经过安全视图转换：不返回 `upstream_model`，目标引用使用稳定 opaque ID；内部完整快照只能用于租户隔离的 Replay。
- Replay 差异必须检测 Provider、上游模型或 endpoint 映射变化，即使 target ID 未变；只返回稳定路径和 `changed` 差异码，不返回隐藏映射原值。
- Replay 差异还必须检测目标能力、流式支持、质量/成本等级、上下文窗口、数据等级或价格元数据变化；只返回 `targets[i]/policy`，不返回价格和内部配置值。
- Decision Journal 保存和读取时都必须重新计算 `input_hash`/`plan_hash`，并校验 PostgreSQL 摘要列与 JSONB 内容一致；检测到篡改必须失败，不能返回部分可信的历史计划。
- 配置摘要和配置 diff 也必须使用稳定 opaque 目标引用；目标 ID 可能由 `provider:upstream_model` 派生，不能直接进入控制面响应或 diff 路径。
- 配置 diff 必须覆盖 Provider、上游模型和 `endpoint_id` 绑定变化；只返回字段路径和变化类型，不返回 endpoint 或其他配置值。
- Run 和 Request 的 HTTP 响应必须经过安全 DTO 转换：不返回 `tenant_id`、幂等键、请求哈希、租约字段或 Provider 内部 Attempt 字段；客户端只读取生命周期、结算和决策关联状态。
- 配置审批 HTTP 响应必须经过安全 DTO 转换：不返回发布幂等键、请求哈希或租户字段，只返回审批生命周期和非敏感执行者标识。
- 访问日志的 Path 只能使用固定路由类别，动态或未知路径必须归并，不能把用户输入原样写入日志。
- 错误响应只返回稳定原因和错误码；未知模型、租户标识或其他用户输入不能直接拼入错误消息。
- Chat 解析器对未知消息角色和未知 JSON 字段返回固定错误消息；内部错误类型可以保留字段用于测试，但公共响应不得回显字段值。
- 版本命令、健康检查命令、离线 `validate` 配置预检、`diff` 配置影响分析、`explain` 决策解释命令、Docker、冒烟脚本、基准测试和 CI。
- `limen demo` 和 `make demo` 必须保持完全离线、确定性，不读取密钥、不访问网络、不输出 Prompt 或真实上游模型名。

明确不包含：每日额度和超额拦截、模型文件热加载、第三个 Provider、同目标自动重试、动态权重、成本路由、分布式熔断、Secret Manager 接入、遥测可视化后端和大型管理后台。

## 工程原则

1. 核心 HTTP 数据面不使用 Web 框架；外部依赖只允许用于 PostgreSQL 驱动和 OpenTelemetry 等明确边界，接口由真实替换需求或测试需求驱动。
2. `context.Context` 必须贯穿 HTTP、Router 和 Provider；客户端断开要取消上游。
3. 一次请求只创建一个总预算；每个目标最多调用一次；SSE 返回成功后不切换。
4. 只有 Provider 归一化为 `retryable_transient` 的响应和传输错误触发 Fallback；认证、配额和确定性错误直接返回。
5. 响应体及时关闭，流式数据有界读取，不复制完整 Prompt、Response 或密钥；普通响应和 SSE 必须传播上游读取或客户端写入错误，已开始的响应不得 Fallback，传输中断必须把 Settlement 标记为 `partial`。Provider 同时返回响应和错误时，Executor 必须关闭未消费的响应体；Anthropic SSE 错误事件或缺少 `message_stop` 时不得伪造 `[DONE]`。
6. 优先整理和复用旧实现，保持文件职责单一，删除已失效代码。
7. 金额使用十进制定点整数；缺失用量或价格时省略费用，不把未知值写成零。
8. Decision Engine 只消费带版本的输入快照，不读取时间、网络或数据库；Router 负责执行计划和并发熔断探测，Provider 只负责协议转换。
9. 生产出站请求必须经安全 Client：HTTPS allowlist、无环境代理、无自动重定向，并拒绝 loopback、私网、链路本地和元数据地址。
10. Run 的 soft budget 只在结算后影响后续准入；不得在 Provider 调用中途按预计费用截断当前响应，也不得把未知费用写成零。
11. 所有受治理 Store 方法必须显式接收 tenant_id；跨租户资源不能只依赖单列 ID，账本以 `(tenant_id, request_id)` 幂等。
12. Run 创建后固定 `strategy` 和 `config_version`；每次受治理 Chat 必须按该版本加载目录和路由参数，不能因线上发布切换配置；请求中的策略只能与 Run 一致，冲突必须返回 `strategy_conflict`，不能静默覆盖。
13. `suspended_accounting` 期间允许记录 `complete_requested` 但不得直接完成；所有未知费用处置完毕后才按固定优先级恢复或进入 `completed`。
14. 生产 PostgreSQL 事务连接池必须通过 `store.OpenPostgres` 设置有限 I/O 期限；`LISTEN/NOTIFY` 专用监听器除外，禁止为业务 Store 重新使用裸 `sql.Open("postgres", ...)`。
15. Trace 属性必须采用固定白名单；禁止记录上游模型名、原始错误、正文和密钥，也禁止传播可能携带任意用户数据的 Baggage。`limen.model.id` 必须来自执行计划中的逻辑模型或兼容模式固定前缀，不能直接写入请求中的模型字符串。Metrics、Trace 和日志中的目标标识必须使用 `catalog.OpaqueTargetID`；内部 Attempt/结算记录可保留真实映射，但观测字段不能通过派生 `target_id` 间接泄露上游模型名。

## Provider 与路由

- Provider 只接收 `internal/provider.ChatRequest` 和 Context，负责一次协议调用及转换，不依赖 HTTP Handler，也不负责模型映射。
- `ModelRegistry` 保存逻辑模型、有序目标和兼容模式；Router 只生成带版本的 ExecutionPlan，Gateway Executor 负责按计划替换上游模型、管理预算、熔断和 Fallback。
- `internal/catalog` 保存目标能力、质量/成本等级、上下文窗口和数据等级；`internal/decision` 负责硬约束过滤（包括 `minimum_quality_tier`）、稳定排序、原因码及 `InputHash`/`PlanHash`。受治理请求的 Run 快照必须原样进入 DecisionInput，不能在 HTTP 层或 Provider 层隐藏修改策略。
- `Router.ChatWithContract` 先生成 ExecutionPlan，再按计划执行；Half-Open 探测权在执行前再次原子获取，竞争失败记录 `skipped_due_to_race`。
- Provider 映射使用名称到实例的只读映射。Provider 适配层必须把上游状态归一为稳定错误分类，只有 `retryable_transient` 允许 Fallback；新增真实 Provider 时必须覆盖请求转换、普通响应、SSE、错误分类、超时和取消测试。
- Provider 的 `ChatRequest` 只携带非敏感 `EndpointID` 绑定标识，不携带 URL；新增或修改目标映射时必须测试 endpoint ID 传播、发布阶段错绑拒绝和 Provider 调用前错绑拒绝。
- `Router.ReplaceRegistryWithPolicy` 是配置发布的唯一切换入口；切换必须在锁内替换目录、路由策略和熔断器快照，只保留当前目录目标和仍有执行引用的历史目标。Executor 开始执行计划时获取熔断器引用，结束后释放；配置发布次数不能导致历史熔断器无界增长，也不能回收仍在执行旧 Run 的目标。熔断器只保存目标健康状态，阈值和冷却时间必须从本次固定 Policy 读取，不能依赖熔断器首次创建时的旧配置。
- `Router.ExplainWithRegistry` 和 `ReplayWithRegistry` 必须复用与发布相同的 endpoint 绑定校验。
- `internal/auth` 负责常量时间校验静态 Bearer Key，并生成带租户和 Scope 的 Principal；HTTP 层按接口声明所需 Scope，控制面不信任请求中的租户字段。
- Provider 负责协议级 Usage 采集，Gateway 负责 attempt 汇总和成本计算；新增 Provider 必须覆盖普通/SSE 用量、缺失用量和取消场景。
- Provider 出错时不提供可消费响应；Executor 仍须防御性关闭同时返回的非空 Body，避免异常适配器泄漏上游连接。
- Provider 出站统一使用 `internal/provider/client.go` 的安全 HTTP Client；构造函数未注入 Client 时也必须自动创建带 endpoint allowlist 的安全 Client，禁止回退到 `http.DefaultClient`；测试可注入 `httptest` Client。
- 配置发布通知只允许携带租户和版本哈希；实例收到通知后必须从数据库重新读取配置，不能信任通知正文，且必须保留通知丢失后的轮询或重启恢复路径。
- 配置发布必须携带 `Idempotency-Key`；幂等记录绑定租户、固定操作和请求哈希，重试不得重复切换版本，冲突必须返回稳定错误。
- 控制面 `Idempotency-Key` 先裁剪首尾空白，再限制为最多 256 字节的可见 ASCII；空值、超长值和控制字符统一按缺少幂等键拒绝，避免索引膨胀和跨客户端规范化差异。
- Provider 密钥通过 `SetAPIKey` 原子替换；加密存储只能返回短暂明文给对应适配器，禁止写入日志、决策快照或 HTTP 响应。启用租户凭据解析时，`ChatRequest` 只携带非敏感租户标识，Router/Decision 不得持久化或读取实际密钥。
- 凭据控制 API 只接受 `admin` Scope，并强制校验固定 provider 与 endpoint 绑定；轮换先加密持久化再更新内存 Provider，撤销同时清除当前实例密钥，响应只返回元数据。PostgreSQL `NOTIFY` 只用于跨实例刷新且失败不得回滚事务，数据库记录仍是唯一事实来源。
- API Key 控制 API 只接受 `admin` Scope；创建、列表、轮换和撤销不接受请求中的租户字段，租户必须来自 Principal。创建或轮换明文只在首次成功响应出现，重试不能从数据库恢复明文。
- PostgreSQL Repository 只能使用参数化 SQL 和事务锁；不保存 Prompt、Response、Tool 正文或明文 Provider Key。迁移必须保留组合外键、RLS 和状态约束。
- 生产方法必须有简体中文用途注释，说明职责、边界或非显然原因；注释保持简短，代码优先通过命名和拆分保证可读性。
- HTTP Handler 依赖装配统一使用 `httpapi.HandlerOptions`；旧的长构造函数只保留兼容包装，不在业务代码中继续增加位置参数。
- OpenAI 兼容边界以 `docs/openai-compatibility.md` 为单一文档来源；新增或拒绝字段必须同步解析器、测试和矩阵，不能静默丢弃未知字段。
- `make compatibility` 必须覆盖矩阵中的支持字段（包括 `max_completion_tokens` 和 `stream_options.include_usage`）、字段互斥、明确拒绝字段、OpenAI 错误 envelope 和 SSE 结束语义；兼容行为变化必须先更新矩阵与契约测试。
- `make reliability` 必须保持为离线、确定性的故障注入入口，覆盖瞬时错误 Fallback、确定性错误不切换、总预算、客户端取消、流式不重放和 Provider 错误分类；可靠性边界变化必须同步更新对应契约测试。

## 测试与验证

- 新行为先写能复现边界的失败测试，再写最小实现；测试聚焦可观察行为，辅助函数保持少而清楚。
- Provider 使用 `httptest.Server`，不访问真实网络或密钥；Router 使用固定 Provider 验证预算、熔断、Fallback 和 SSE 边界。
- Provider 普通 JSON、错误正文和 SSE 事件必须有界读取；新增转换路径必须覆盖超大响应/事件被拒绝、上游 SSE 错误/截断和响应体关闭，不得为了观察用量而无界缓存。
- Decision Engine 测试必须覆盖能力、质量下限、流式、上下文、数据等级等硬过滤、策略排序、稳定原因码和软预算策略切换；100 组已提交 golden fixture 必须验证重建 Engine 后的规范计划字节与 `plan_hash`，不得静默更新预期值。
- API 测试必须覆盖未知字段和暂不支持字段的 `unsupported_field`、Limen 契约错误，以及 `model=auto` 的可观察计划结果。
- API 测试必须验证成功、Dry Run 和 `no_eligible_target` 响应的 `X-Limen-Plan-Hash` 与计划摘要一致；日志和 Trace 只能通过固定白名单记录该摘要。
- Trace 测试必须验证兼容模式的 `limen.model.id` 使用固定前缀而非原始客户端模型名；显式模型也必须以执行计划确认的逻辑 ID 为准。
- Dry Run 测试必须证明不调用 Provider、不改变熔断状态，并返回稳定的计划哈希和候选原因。
- 配置版本预演测试必须证明读取指定草稿、返回对应 `config_version`，且不改变当前 Router、熔断状态或 Provider 调用计数。
- 配置影响分析测试必须证明历史输入和草稿目标均被正确使用，返回 Provider/策略变化但不泄露上游模型名，且不调用 Provider、不改变当前 Router。
- Decision Journal 测试必须覆盖租户隔离、同 ID 幂等、保存/读取哈希校验、持久化摘要与 JSONB 不一致、Explain、Replay 不访问 Provider 以及算法版本不可用错误。
- Run HTTP 测试必须覆盖创建/查询/完成/取消、同键幂等、请求准入、每个 Fallback 目标独立 Attempt 边界、无 Attempt 的零成本结算、已知成本结算和未知成本 `pending`；已处理幂等重试还必须返回安全的原 Request ID、Decision ID 和结算状态，并证明决策标识在 Provider 调用前已绑定。
- Run HTTP 返回测试必须证明公共响应不泄露租户标识、幂等键、请求哈希和租约信息。
- 未知费用处置测试必须覆盖补记金额、接受未知、重复幂等键、跨 Run 请求绑定和 `admin` Scope；补记最多产生一条 Ledger。
- Run 租约测试必须覆盖同一请求的抢占拒绝、续租、响应后释放、过期恢复、`abandoned/pending` 和 Run `suspended_accounting`；租约获取失败且没有 Attempt 时必须验证零成本结算和 Run 仍为 active，并用竞态测试验证后台恢复；HTTP 幂等测试还必须覆盖 `request_in_progress`（原 Request ID 与 `Retry-After`）、`request_already_processed`、`idempotency_conflict`，并证明重复请求不会再次调用 Provider。
- 跨实例取消测试必须覆盖取消事件租户隔离、在途 Context 取消、`run_cancelled` 错误和重复取消幂等。
- 鉴权测试必须覆盖错误 Key、未知 Scope、Scope 拒绝、Principal 租户绑定，以及带 Run Header 的 Chat 额外 `runs:write` 校验。
- API Key Store 测试必须覆盖格式解析、HMAC 摘要、过期/停用 Key、Scope 解析和跨租户查询不泄露。
- 配置控制面测试必须覆盖严格解析、版本幂等、租户隔离、发布替换、策略切换、结构化 diff 和 `/v1/limen/configs` Scope。
- 配置 diff 测试必须覆盖 endpoint 绑定变化，并证明响应不包含 endpoint 原值。
- 配置 dry-run/replay 测试必须覆盖错绑 endpoint 的稳定错误码，并证明不访问 Provider、不切换当前 Router。
- Run 配置版本测试必须证明发布新版本后，已有 Run 仍使用创建时的目录、价格和路由参数；版本缺失时返回 `config_version_unavailable`，不得静默降级到当前目录。
- 配置测试必须覆盖环境密钥、`*_FILE` 文件密钥、来源冲突、空文件、读取失败和 PostgreSQL Key Store 对静态 Key 文件的拒绝；测试错误不得包含密钥内容。
- 配置控制面安全视图测试必须证明摘要和 diff 不泄露由上游模型派生的目标标识。
- 配置审批测试必须证明响应不泄露发布幂等键、请求哈希和租户字段。
- 配置审批测试必须覆盖默认关闭回归、批准者身份分离、过期、状态冲突、绑定冲突、并发幂等、Router 激活失败重试和 PostgreSQL RLS；不得把审批校验只放在 HTTP 层而绕过持久化事务。
- 算法版本测试必须覆盖当前版本解析、未知版本拒绝和重复注册拒绝；配置 diff 测试必须证明只返回稳定路径与变化类型。
- Replay 算法注册必须支持显式保留截止时间；过期版本返回 `algorithm_version_unavailable`，不得静默回退；新增算法版本必须保留旧版本语义或明确退役窗口。
- 凭据存储测试必须覆盖 AES-GCM 解密、租户/Provider/endpoint 绑定、轮换、撤销和密文不包含明文；指标测试必须覆盖固定名称、有界标签、未知模型归并、真实 Attempt 语义和 admin 鉴权。
- 指标测试还必须覆盖空 Registry 的 `HELP`/`TYPE` 元数据、计数器和 Histogram 的 Prometheus 格式，以及标签序列上限。
- 凭据控制面测试必须覆盖 admin Scope、endpoint 不匹配拒绝、轮换后立即生效、撤销清除内存密钥以及响应不包含明文。
- API Key 控制面测试必须覆盖 Scope、幂等冲突、明文只返回一次、轮换后旧 Key 立即失效、新 Key 生效、跨租户前缀猜测、RLS 和数据库摘要不含明文；审计测试还必须证明 actor_id 不包含原始 Key。
- 跨实例凭据刷新必须只传递租户、Provider、endpoint 和撤销状态等元数据，通知丢失时不能破坏数据库事实或引入明文。
- 出站安全测试必须覆盖配置层 HTTPS、allowlist、重定向、代理关闭、私网/CGNAT/保留测试网地址拒绝；测试不得真的访问外部 Provider。
- 用量和成本测试必须覆盖定点计算、Fallback 汇总、部分结算、Trailer 和日志敏感信息；SSE 测试要证明第一段数据无需等待完整响应。
- 结算失败测试必须覆盖短退避重试、已知成本快照保留、未知费用停止重试、`pending` 查询事实和租约恢复不重复记账。
- Trace 测试必须覆盖同一 Trace ID 的请求、准入、决策、Fallback Attempt 和结算，并用哨兵值证明正文、密钥和上游模型名不会进入 Span。
- 流式测试必须证明首段 Flush 不等待完整响应，并且日志/Trace 的 TTFB 在有正文时出现、无正文时省略。
- PostgreSQL 集成测试必须使用非超级用户验证 RLS，并覆盖 100 并发准入、并发幂等、唯一账本、强制终止独立执行进程、数据库暂停/恢复、多个 Store 竞争租约恢复以及取消通知的轮询兜底；不得用 SQL Mock 代替数据库不变量。
- 提交前运行 `make check`；交付前额外运行 `go clean -testcache`、`make integration`、`make build`、`make smoke`、`make bench` 和 `git diff --check`。
- `make bench` 必须同时覆盖固定 100 个候选目标的纯决策路径和 Router 主/Fallback 路径；决策基准不得访问网络或数据库，文档记录的机器与结果必须来自实际运行。
- 兼容性相关改动还必须运行 `make compatibility`，并确认不访问真实 Provider 网络。
- Fallback、超时、取消、熔断或 Provider 错误分类相关改动还必须运行 `make reliability`，并确认不访问真实 Provider 网络。
- 修改演示场景时还必须运行 `make demo`，并保持输出字段和安全边界稳定。
- 修改离线 `explain` 命令时必须验证相同模型目录和请求快照得到相同 `plan_hash`，且输出不包含 Prompt、密钥或真实上游模型名。
- 修改离线 `validate` 命令时必须证明不读取密钥、不访问网络，成功输出只包含 `config_version` 和数量摘要，不泄露 `upstream_model`；endpoint 错绑必须在预检阶段失败且不回显 endpoint 原值。
- 修改离线 `diff` 命令时必须证明两个配置都经过严格解析，输出版本、变化类型和 opaque 目标引用，不泄露 `upstream_model`、endpoint 原值、价格或密钥；命令不得连接数据库或访问 Provider。`--fail-on` 门禁必须只按稳定类别和安全路径判断，命中时输出 JSON 后返回非零退出码。

## 文档同步

- 文档正文统一使用简体中文；代码、命令、API、环境变量和必要技术名词保留原文。
- API、配置、启动方式或用户可见行为变化：同步更新 `README.md`。
- 架构、数据流、错误分类或关键设计变化：同步更新 `docs/design.md`。
- 开发流程、测试要求或注释规则变化：同步更新本文档。
- 同一次改动中保持计划、设计、示例配置和实现一致；不保留已被新语义替代的旧说明。
