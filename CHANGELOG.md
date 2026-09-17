# 变更记录

## v0.3.0

### 新增

- 租户隔离的不可变配置版本、配置发布 API、Router 原子快照切换和配置版本恢复。
- AES-GCM Provider 凭据存储边界、endpoint 绑定和并发安全密钥轮换。
- `admin` Scope 保护的 Provider 凭据轮换和撤销 API，响应只返回安全元数据。
- 需要 `admin` Scope 的基础 Prometheus 文本指标和有界标签。

### 限制

- 配置文件不热加载；Secret Manager、跨实例凭据变更通知、完整 OpenTelemetry 导出和每日额度仍未实现。

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
