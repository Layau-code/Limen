# OpenAI Chat Completions 兼容矩阵

Limen 兼容的是 OpenAI Chat Completions 的受支持子集，不宣称完整兼容 Responses API 或全部高级字段。客户端只需把 `base_url` 指向 Limen，并使用 Limen 认证 Key。

## 请求字段

| 字段 | 状态 | 说明 |
| --- | --- | --- |
| `model` | 支持 | 配置模式使用逻辑模型 ID；兼容模式透传 `gpt-*`、`o1-*`、`o3-*`、`claude-*`。 |
| `messages[].role` | 支持 | `system`、`user`、`assistant`。 |
| `messages[].content` | 支持 | 纯文本字符串。 |
| `max_tokens` | 支持 | 由 Provider 适配器转换。 |
| `temperature` | 支持 | 由 Provider 适配器转换。 |
| `stream` | 支持 | 普通响应或 OpenAI 风格 SSE。 |
| `stream_options.include_usage` | 支持 | 仅在 `stream=true` 时接受布尔值；控制 OpenAI SSE 是否请求最终用量事件。 |
| `limen` | Limen 扩展 | 能力、数据等级和 `balanced/economy` 策略契约。 |
| `tools`、`tool_choice` | 明确拒绝 | 返回 `400 unsupported_field`。 |
| `response_format` | 明确拒绝 | 当前不实现结构化输出。 |
| `n`、`logprobs` | 明确拒绝 | 当前不实现多候选和 Logprobs。 |
| `messages[].tool_calls` | 明确拒绝 | 当前不实现工具调用。 |
| 图片、音频和其他多模态内容 | 明确拒绝 | `content` 必须是文本字符串。 |
| 未知字段 | 明确拒绝 | 严格 JSON 解码，不静默丢弃。 |

## 响应与行为

- 普通响应保持 OpenAI Chat Completion 结构；Anthropic 响应由适配器转换后再返回。
- 流式响应使用 `text/event-stream`，文本增量转换为 OpenAI SSE，结束输出 `data: [DONE]`。
- Provider 的 429、5xx 和传输瞬时错误可按 ExecutionPlan 切换备用目标；已经开始输出的 SSE 不重放。
- `X-Limen-Provider`、`X-Limen-Attempts`、`X-Limen-Route` 和 `X-Limen-Decision-ID` 是 Limen 扩展响应头。
- `/v1/chat/completions` 与 `/v1/models` 都要求 Bearer Key；错误使用 OpenAI 风格 `error.type`、`error.code` 和 `error.message`。

## 不在 1.0 范围

Responses API、Assistants API、Tools、结构化输出、Vision、多模态、Embedding 和第三 Provider 不属于当前兼容矩阵。新增能力前必须先完成 Provider 转换、能力目录、决策契约和普通/SSE/错误/取消测试。

## 自动化契约

运行 `make compatibility` 可执行验证本矩阵中的核心边界：支持的 Chat 请求字段、`stream_options.include_usage` 约束、Tools/结构化输出/未知字段拒绝、OpenAI 风格错误 envelope，以及流式响应的 `text/event-stream` 和 `[DONE]` 结束语义。测试只使用进程内 Provider，不访问真实网络。
