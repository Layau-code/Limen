# Limen 开发协作规范

## 项目定位

Limen 是面向 Agent 的 Go AI Gateway：以 OpenAI 兼容 API 接收请求，用逻辑模型注册表选择 OpenAI 或 Anthropic，并在预算内安全执行有序 Fallback。项目强调可解释、可测试和小而清晰的实现，不为展示技术栈增加复杂度。

## 当前交付范围

- `POST /v1/chat/completions`、`GET /v1/models`、`/livez`、`/readyz`。
- OpenAI 与 Anthropic 协议适配，普通响应和 SSE 流式响应。
- 启动时严格加载的只读模型注册表；未配置时保留四种前缀兼容模式。
- 共享请求预算、单次尝试超时、按目标熔断、瞬时故障 Fallback、路由摘要和安全日志。
- 版本命令、健康检查命令、Docker、冒烟脚本、基准测试和 CI。

明确不包含：配置热加载、第三个 Provider、同目标自动重试、动态权重、成本路由、分布式熔断、数据库、管理后台和完整遥测平台。

## 工程原则

1. 只使用 Go 标准库；接口由真实替换需求或测试需求驱动。
2. `context.Context` 必须贯穿 HTTP、Router 和 Provider；客户端断开要取消上游。
3. 一次请求只创建一个总预算；每个目标最多调用一次；SSE 返回成功后不切换。
4. 只有固定瞬时状态（408、409、429、500、502、503、504、529）和传输错误触发 Fallback；确定性错误直接返回。
5. 响应体及时关闭，流式数据有界读取，不复制完整 Prompt、Response 或密钥。
6. 优先整理和复用旧实现，保持文件职责单一，删除已失效代码。

## Provider 与路由

- Provider 只接收 `internal/provider.ChatRequest` 和 Context，负责一次协议调用及转换，不依赖 HTTP Handler，也不负责模型映射。
- `ModelRegistry` 保存逻辑模型、有序目标和兼容模式；Router 替换上游模型、管理预算、熔断和 Fallback。
- Provider 映射使用名称到实例的只读映射。新增真实 Provider 时必须覆盖请求转换、普通响应、SSE、错误、超时和取消测试。
- 生产方法必须有简体中文用途注释，说明职责、边界或非显然原因；注释保持简短，代码优先通过命名和拆分保证可读性。

## 测试与验证

- 新行为先写能复现边界的失败测试，再写最小实现；测试聚焦可观察行为，辅助函数保持少而清楚。
- Provider 使用 `httptest.Server`，不访问真实网络或密钥；Router 使用固定 Provider 验证预算、熔断、Fallback 和 SSE 边界。
- 提交前运行 `make check`；交付前额外运行 `go clean -testcache`、`make build`、`make smoke`、`make bench` 和 `git diff --check`。

## 文档同步

- 文档正文统一使用简体中文；代码、命令、API、环境变量和必要技术名词保留原文。
- API、配置、启动方式或用户可见行为变化：同步更新 `README.md`。
- 架构、数据流、错误分类或关键设计变化：同步更新 `docs/design.md`。
- 开发流程、测试要求或注释规则变化：同步更新本文档。
- 同一次改动中保持计划、设计、示例配置和实现一致；不保留已被新语义替代的旧说明。
