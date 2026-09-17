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

### 修复

- PostgreSQL Request 读取可正确处理尚未生成 `decision_id` 的准入状态。
- 过期租约恢复会把遗留的 `Attempt started` 标记为 `abandoned`；PostgreSQL 事务连接池设置 5 秒网络 I/O 期限，避免断连时永久阻塞。
- 客户端提供的 Request ID 先转换为长度固定的不可逆摘要再进入日志与 Trace，避免借标识字段注入敏感正文。

### 限制

- 旧算法保留窗口与审批审计、Secret Manager 和遥测可视化后端仍待后续生产化阶段。

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
