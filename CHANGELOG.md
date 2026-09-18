# 变更记录

## 未发布

### 新增

- 受治理 Chat 的已处理幂等重试现在返回原 Request ID、Decision ID 和结算状态；决策标识在 Provider 调用前持久化绑定，避免重试丢失审计关联。
- 新增离线 `limen diff`，在发布审批前比较两个模型目录的安全结构影响；输出配置版本、变化类型和 opaque 目标引用，不访问数据库或 Provider。
- `limen diff` 新增 `--fail-on` 发布风险门禁，可按稳定变化类别阻止 Provider、endpoint、策略或路由变化，并保留安全 JSON 输出。
- Replay 差异现在能识别同一 target ID 下的 Provider、上游模型、endpoint 或目标策略元数据变化，同时只返回安全差异码，不泄露隐藏映射和价格。
- `make bench` 现在覆盖固定 100 个候选目标的完整 Decision Engine 路径，单独测量能力过滤、稳定排序和计划哈希开销。
- 新增离线 `limen validate`，在不读取密钥或访问网络的情况下预检模型目录并输出稳定配置版本摘要。
- 新增 `make validate` 和 CI 预检步骤，确保示例模型目录在构建阶段通过严格校验。
- `limen validate` 现在复用启动阶段的 Provider endpoint 绑定校验，错绑配置不会通过发布前预检。
- Anthropic 普通 JSON 响应新增 4 MiB 有界解码，超大响应在协议转换阶段拒绝，避免异常上游响应造成无界内存增长。
- OpenAI 和 Anthropic 的非成功错误正文现在最多透传 64 KiB，避免异常错误响应拖垮客户端或网关内存。
- 配置 diff 现在会报告目标 `endpoint_id` 绑定变化，只返回安全路径和变化类型。
- 配置 dry-run 和 replay 现在复用发布阶段的 endpoint 绑定校验，错绑配置会在预演阶段返回 `endpoint_binding_mismatch`。
- 模型目标新增可选 `endpoint_id` 绑定；Router 发布和 Provider 出站都会校验目标只能访问进程已配置的 endpoint，避免跨 endpoint 复用凭据或熔断状态。
- 新增配置版本预演接口，可在发布前对指定草稿生成决策计划而不切换 Router 或访问 Provider。
- 新增配置影响分析接口，可将历史决策重放到指定草稿并返回安全结构化差异，不访问 Provider 或改变线上路由。
- 新增完全离线的 `limen demo`，展示 Fallback 和配置草稿影响分析，便于重复验收。
- 新增完全离线的 `limen explain`，使用固定评估时间输出候选淘汰原因和稳定计划哈希；没有可用目标时也返回 `no_eligible_target` 证据。
- 新增 `make compatibility` OpenAI Chat 兼容契约测试，自动验证请求字段、错误 envelope 和 SSE 边界。
- 新增 `make reliability` 离线故障注入契约，集中验证 Fallback、预算、取消、流式不重放和 Provider 错误分类。
- 密钥配置新增 `LIMEN_API_KEY_FILE`、`OPENAI_API_KEY_FILE` 和 `ANTHROPIC_API_KEY_FILE`，支持容器只读 Secret 挂载并拒绝明文/文件来源冲突。
- Prometheus 指标在无样本时也输出稳定的 `HELP`/`TYPE` 元数据，保持空闲实例的指标契约不漂移。
- OpenAI 兼容契约支持 `stream_options.include_usage`，并严格校验流式条件和嵌套未知字段。
- OpenAI 兼容契约支持 `max_completion_tokens`，保留新版 OpenAI 字段语义，并拒绝与 `max_tokens` 同时出现。
- OpenAI 兼容契约支持 `developer` 消息角色；OpenAI 原样转发，Anthropic 转换为顶层 `system`。
- Chat 和 Dry Run 响应新增 `X-Limen-Plan-Hash`，并把固定摘要接入安全访问日志和 Trace；无可用目标的错误也保留计划证据。
- 受治理 Chat 明确幂等重试契约：执行中的重复请求返回原 Request ID 和 `Retry-After`，完成后重试与请求哈希冲突使用稳定错误码，且不会重复调用 Provider。
- 受治理 Chat 现在把 Run 的预算、已结算费用、剩余截止时间和路由阈值写入 DecisionInput，预算接近阈值时的 `economy` 策略和截止时间过滤不再只停留在离线算法中。
- 受治理 Run 现在按创建时固定的 `config_version` 加载目录和路由参数；配置发布后，已有 Run 不会静默切换到新模型目录。
- Decision Journal 读取时新增 `plan_hash` 和持久化摘要列校验，检测 JSONB 证据篡改后拒绝 Replay。
- Decision Engine 将 `minimum_quality_tier` 作为硬约束，并新增稳定 Provider 错误分类。
- Fallback 的每次真实 Provider 调用都在调用前写入独立 Attempt，Run 策略不能被请求覆盖。
- PostgreSQL Run 取消支持 `LISTEN/NOTIFY` 低延迟广播，并保留事件表轮询兜底。
- 未知费用支持管理员幂等补记或明确接受，Run 可从 `suspended_accounting` 恢复；PostgreSQL 保存处置摘要。
- Provider 返回的非敏感 request ID 会补写到对应 Attempt，保留上游调用证据。
- PostgreSQL 新增 008 迁移，对所有租户表启用 `FORCE ROW LEVEL SECURITY`。
- 真实 PostgreSQL 集成测试覆盖非超级用户 RLS、100 并发准入与幂等、唯一账本、双 Store 租约恢复竞争和取消事件双路径。
- 故障注入会强制终止持有租约的独立进程，并暂停/恢复 PostgreSQL 验证结算任务不会丢失或重复记账。
- 可选 OTLP/HTTP Trace 将 HTTP、Run 准入、Decision、Fallback Attempt 和 Settlement 串成隐私安全的证据链。
- Metrics 增加可信模型、状态类别、稳定拒绝码和目标标签，Attempt 计数改为只反映真实 Provider 调用。
- Decision Engine 增加 100 组已提交输入与计划哈希的 golden Replay 语料，并提供确定性生成器。
- Provider 出站安全测试覆盖环境代理绕过、云元数据地址和非法 endpoint URL；endpoint 辅助函数拒绝用户信息、查询参数和片段。
- PostgreSQL 配置发布增加跨实例版本通知和 5 秒轮询兜底，其他实例会从数据库重新读取并原子刷新 Router。
- 结构化日志和 HTTP Trace 增加首字节延迟 `ttfb_ms`，没有响应正文时不会伪造该字段。
- 访问日志将未知和动态 URL Path 归并为固定路由类别，避免用户输入借路径字段进入日志。
- 新决策使用 `decision.v2` 规范化能力和数据等级集合，同时保留 `decision.v1` 算法注册以兼容历史 Replay。
- Replay 差异从单一哈希错误升级为安全结构化差异，包含策略、逻辑目标顺序、候选原因和哈希，不泄露上游模型名。
- 新增租户隔离的控制面审计摘要和 `GET /v1/limen/audit`，重复控制操作按稳定事件 ID 去重。
- Algorithm Registry 支持显式 Replay 保留截止时间，过期算法返回 `algorithm_version_unavailable`，不回退到新版本。
- 将 Gateway Executor 从 Router 计划生成逻辑中拆出；Executor 只按 ExecutionPlan 执行 Provider，不重新选择模型。
- PostgreSQL API Key Store 新增创建、列表、原子轮换和撤销控制面；明文只在首次创建或轮换响应返回，认证查询使用受控函数，管理操作使用 RLS 和幂等记录。
- 控制面审计新增非敏感 `actor_id`，可关联静态身份或 API Key 公开前缀，不保存原始凭据。
- 配置发布新增可选双人审批；审批绑定配置版本、发布幂等键和请求哈希，PostgreSQL 在同一事务内完成审批消费与配置切换，并覆盖过期、并发幂等和 RLS 测试。
- Provider 基础地址配置现在强制使用 HTTPS；安全拨号器额外拒绝 CGNAT、保留测试网和广播地址，避免配置层与运行时出站策略不一致。
- Provider 出站凭据现在绑定请求租户；启用加密凭据存储后，OpenAI/Anthropic 每次调用按租户和 endpoint 解析密钥，缺失凭据不会回退到进程共享密钥。
- HTTP Handler 新增 `HandlerOptions` 依赖装配入口，主程序不再使用难以维护的长参数构造调用；旧入口保留为兼容包装。
- `make check` 新增 Replay fixture 生成一致性校验，防止决策证据文件与生成器悄然漂移。
- Dry Run、Decision 查询和 Replay 响应现在隐藏真实上游模型名，并将目标引用归并为稳定 opaque ID；内部快照仍可用于确定性 Replay。
- 新增 OpenAI Chat Completions 兼容矩阵，明确支持子集、拒绝字段、SSE 行为和 1.0 范围，避免把有限兼容误写成完整兼容。
- 未知 Chat 请求字段现在统一返回 `unsupported_field`，与已知暂不支持字段保持一致。
- 运维文档同步说明 HTTPS 出站和加密租户凭据模式，避免把环境变量密钥误当成多租户回退。
- Metrics 和 Trace 的目标标识统一改为稳定 opaque ID，避免默认派生的 `provider:upstream_model` 进入观测系统。
- Trace 的逻辑模型属性改为读取执行计划确认的模型 ID；兼容模式只记录固定前缀，避免任意客户端模型字符串进入观测系统。
- Run 和 Request 控制面改用安全响应 DTO，隐藏租户标识、幂等键、请求哈希和租约内部字段。
- 配置摘要与 diff 路径改用 opaque 目标引用，避免默认派生的 `provider:upstream_model` 泄露到控制面响应。
- 配置审批接口改用安全状态 DTO，隐藏发布幂等键、请求哈希和租户字段。
- 未注册模型的 HTTP 错误不再回显客户端提供的模型字符串。
- README 的基准示例同步到当前 `make bench` 实测结果。

### 修复

- 控制面幂等键现在限制为最多 256 字节的可见 ASCII，并拒绝控制字符，避免异常键造成索引膨胀或规范化差异。
- Provider 构造函数在未注入 HTTP Client 时现在自动使用 endpoint allowlist 安全 Client，避免意外绕过代理、重定向和出站地址限制。
- 修复受治理请求在租约获取失败且尚未调用 Provider 时错误进入未知费用的问题；现在按零成本完成并释放 Run 并发名额。
- 修复受治理 Chat 的结算失败清理路径：已知成本不会被延迟重试降级为未知费用，非重试型未知结算也不会重复创建结算 Trace；没有发起 Provider Attempt 的请求按零成本完成，不会误暂停 Run。
- PostgreSQL Request 读取可正确处理尚未生成 `decision_id` 的准入状态。
- 过期租约恢复会把遗留的 `Attempt started` 标记为 `abandoned`；PostgreSQL 事务连接池设置 5 秒网络 I/O 期限，避免断连时永久阻塞。
- 客户端提供的 Request ID 先转换为长度固定的不可逆摘要再进入日志与 Trace，避免借标识字段注入敏感正文。
- 未注册模型不再原样进入 Metrics，熔断跳过也不再被错误统计为 Provider Attempt。
- 修复定点价格 JSON 编码与解码格式不对称，带价格的 Decision Journal 现在可以从 PostgreSQL 还原并 Replay。

### 限制

- 配置审批默认关闭；静态 Key 无法满足双人身份分离，启用生产审批需使用 PostgreSQL API Key Store。Secret Manager 和遥测可视化后端仍待后续生产化阶段。

## v0.3.0

### 新增

- 租户隔离的不可变配置版本、配置发布 API、Router 原子快照切换和配置版本恢复。
- AES-GCM Provider 凭据存储边界、endpoint 绑定和并发安全密钥轮换。
- `admin` Scope 保护的 Provider 凭据轮换和撤销 API，响应只返回安全元数据。
- 持久化 `settlement_jobs`、带租约的后台结算恢复和跨实例凭据 `NOTIFY` 刷新边界。
- 需要 `admin` Scope 的基础 Prometheus 文本指标和有界标签。

### 限制

- 配置文件不热加载；Secret Manager、完整 OpenTelemetry 导出和每日额度仍未实现。

## v0.2.0

### 新增

- Provider 普通响应和 SSE 的 Token 用量采集。
- 每个上游目标可选输入/输出价格，使用定点整数计算请求成本。
- Fallback attempt 结算汇总、`complete`/`partial`/`unavailable` 状态、HTTP Trailer 和安全结构化日志字段。

### 限制

- 不实现每日额度、超额拦截、账本持久化、账单查询或成本路由。

## v0.1.0

### 新增

- OpenAI 兼容的聊天和模型列表 API，支持 OpenAI 与 Anthropic Claude。
- 逻辑模型注册表、多目标有序 Fallback、共享请求预算和按目标熔断。
- 普通响应与 SSE 转换、客户端取消传播、健康接口、版本和健康检查命令。
- 安全路由响应头、结构化日志、Docker 镜像、冒烟脚本、基准测试和 CI。

### 限制

- 不支持 tools、tool calls、音频和多模态内容。
- 不支持热加载、动态权重、同目标重试、分布式熔断、成本统计、数据库和管理后台。

### 不兼容变化

- 模型文件从单目标字段升级为 `targets` 数组；旧字段会在启动时被拒绝。
- 配置模式下请求必须使用注册表逻辑模型 ID；未配置模型文件才启用前缀兼容模式。
