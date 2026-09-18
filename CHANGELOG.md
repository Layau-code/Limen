# 变更记录

## 未发布

### 新增

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

### 修复

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
