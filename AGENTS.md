# Limen 开发协作规范

## 项目定位

Limen 是面向 Agent 的 Go AI Gateway：以 OpenAI 兼容 API 接收请求，用逻辑模型注册表选择 OpenAI 或 Anthropic，并在预算内安全执行有序 Fallback。项目强调可解释、可测试和小而清晰的实现，不为展示技术栈增加复杂度。

## 当前交付范围

- `POST /v1/chat/completions`、`GET /v1/models`、`/livez`、`/readyz`。
- OpenAI 与 Anthropic 协议适配，普通响应和 SSE 流式响应。
- 启动时严格加载的只读模型注册表；未配置时保留四种前缀兼容模式。
- 能力目录与版本化 Decision Engine：`model=auto` 或 Limen 契约会生成带输入/计划哈希的 ExecutionPlan。
- `POST /v1/limen/decisions/dry-run` 只生成计划，不访问 Provider；模型文件启动时生成稳定 `config_version`，后续 Run 固定引用该版本。
- 阶段 B 的 `internal/run` 和 `internal/store` 已定义 Run/Request/Attempt、幂等和账本边界；无 Run Chat 仍走原有内存结算路径。
- HTTP Run 控制面只有在显式 `LIMEN_RUN_STORE=memory` 时启用；内存实现仅用于开发演示，不能作为生产账本。
- 配置 `LIMEN_DATABASE_URL` 后必须通过 `database/sql` 和参数化 PostgreSQL Repository 启动；迁移只使用版本表执行一次，DSN 不得进入日志。
- 鉴权必须先生成带 `tenant_id` 和 Scope 的 Principal；Provider、Router 和 Store 不得读取原始 API Key。当前静态 Key 由 `LIMEN_API_SCOPES` 限制，默认 Scope 只为兼容单机开发，生产 Key Store 后续替换。
- Chat API 当前只承诺文本消息、普通/SSE、`model`、`max_tokens`、`temperature` 和 `stream`；Tools、tool calls、Vision、多模态、Responses API 与未知字段必须明确返回 `400`。
- 共享请求预算、单次尝试超时、按目标熔断、瞬时故障 Fallback、路由摘要和安全日志。
- 受治理 Request 必须在准入后取得租约，默认 30 秒过期、每 10 秒续租；租约丢失时取消本地 Context，恢复任务只能进入未知费用/暂停账本，不得盲目重放 Provider。
- Provider 用量采集、按目标定点价格计算成本，以及响应结束后的结算 Trailer 和结构化日志。
- 版本命令、健康检查命令、Docker、冒烟脚本、基准测试和 CI。

明确不包含：每日额度和超额拦截、配置热加载、第三个 Provider、同目标自动重试、动态权重、成本路由、分布式熔断、数据库 Key Store、管理后台和完整遥测平台。

## 工程原则

1. 核心数据面只使用 Go 标准库；PostgreSQL 允许使用成熟的单一驱动，接口由真实替换需求或测试需求驱动。
2. `context.Context` 必须贯穿 HTTP、Router 和 Provider；客户端断开要取消上游。
3. 一次请求只创建一个总预算；每个目标最多调用一次；SSE 返回成功后不切换。
4. 只有固定瞬时状态（408、409、429、500、502、503、504、529）和传输错误触发 Fallback；确定性错误直接返回。
5. 响应体及时关闭，流式数据有界读取，不复制完整 Prompt、Response 或密钥。
6. 优先整理和复用旧实现，保持文件职责单一，删除已失效代码。
7. 金额使用十进制定点整数；缺失用量或价格时省略费用，不把未知值写成零。
8. Decision Engine 只消费带版本的输入快照，不读取时间、网络或数据库；Router 负责执行计划和并发熔断探测，Provider 只负责协议转换。
9. 生产出站请求必须经安全 Client：HTTPS allowlist、无环境代理、无自动重定向，并拒绝 loopback、私网、链路本地和元数据地址。
10. Run 的 soft budget 只在结算后影响后续准入；不得在 Provider 调用中途按预计费用截断当前响应，也不得把未知费用写成零。
11. 所有受治理 Store 方法必须显式接收 tenant_id；跨租户资源不能只依赖单列 ID，账本以 `(tenant_id, request_id)` 幂等。

## Provider 与路由

- Provider 只接收 `internal/provider.ChatRequest` 和 Context，负责一次协议调用及转换，不依赖 HTTP Handler，也不负责模型映射。
- `ModelRegistry` 保存逻辑模型、有序目标和兼容模式；Router 替换上游模型、管理预算、熔断和 Fallback。
- `internal/catalog` 保存目标能力、质量/成本等级、上下文窗口和数据等级；`internal/decision` 负责硬约束过滤、稳定排序、原因码及 `InputHash`/`PlanHash`。
- `Router.ChatWithContract` 先生成 ExecutionPlan，再按计划执行；Half-Open 探测权在执行前再次原子获取，竞争失败记录 `skipped_due_to_race`。
- Provider 映射使用名称到实例的只读映射。新增真实 Provider 时必须覆盖请求转换、普通响应、SSE、错误、超时和取消测试。
- `internal/auth` 负责常量时间校验静态 Bearer Key，并生成带租户和 Scope 的 Principal；HTTP 层按接口声明所需 Scope，控制面不信任请求中的租户字段。
- Provider 负责协议级 Usage 采集，Gateway 负责 attempt 汇总和成本计算；新增 Provider 必须覆盖普通/SSE 用量、缺失用量和取消场景。
- Provider 出站统一使用 `internal/provider/client.go` 的安全 HTTP Client；测试可注入 `httptest` Client，但生产装配不得退回 `http.DefaultClient`。
- PostgreSQL Repository 只能使用参数化 SQL 和事务锁；不保存 Prompt、Response、Tool 正文或明文 Provider Key。迁移必须保留组合外键、RLS 和状态约束。
- 生产方法必须有简体中文用途注释，说明职责、边界或非显然原因；注释保持简短，代码优先通过命名和拆分保证可读性。

## 测试与验证

- 新行为先写能复现边界的失败测试，再写最小实现；测试聚焦可观察行为，辅助函数保持少而清楚。
- Provider 使用 `httptest.Server`，不访问真实网络或密钥；Router 使用固定 Provider 验证预算、熔断、Fallback 和 SSE 边界。
- Decision Engine 测试必须覆盖硬过滤、策略排序、稳定原因码、软预算策略切换、至少 10 个 golden fixture 和重复构造后的哈希一致性。
- API 测试必须覆盖未知字段和暂不支持字段的 `unsupported_field`、Limen 契约错误，以及 `model=auto` 的可观察计划结果。
- Dry Run 测试必须证明不调用 Provider、不改变熔断状态，并返回稳定的计划哈希和候选原因。
- Run HTTP 测试必须覆盖创建/查询/完成/取消、同键幂等、请求准入、Attempt 边界、已知成本结算和未知成本 `pending`。
- Run 租约测试必须覆盖同一请求的抢占拒绝、续租、响应后释放、过期恢复、`abandoned/pending` 和 Run `suspended_accounting`，并用竞态测试验证后台恢复。
- 鉴权测试必须覆盖错误 Key、未知 Scope、Scope 拒绝、Principal 租户绑定，以及带 Run Header 的 Chat 额外 `runs:write` 校验。
- 出站安全测试必须覆盖 allowlist、HTTPS、重定向、代理关闭和私网地址拒绝；测试不得真的访问外部 Provider。
- 用量和成本测试必须覆盖定点计算、Fallback 汇总、部分结算、Trailer 和日志敏感信息；SSE 测试要证明第一段数据无需等待完整响应。
- 提交前运行 `make check`；交付前额外运行 `go clean -testcache`、`make build`、`make smoke`、`make bench` 和 `git diff --check`。

## 文档同步

- 文档正文统一使用简体中文；代码、命令、API、环境变量和必要技术名词保留原文。
- API、配置、启动方式或用户可见行为变化：同步更新 `README.md`。
- 架构、数据流、错误分类或关键设计变化：同步更新 `docs/design.md`。
- 开发流程、测试要求或注释规则变化：同步更新本文档。
- 同一次改动中保持计划、设计、示例配置和实现一致；不保留已被新语义替代的旧说明。
