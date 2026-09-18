# Limen OpenAI 兼容契约计划

## 目标

将 `docs/openai-compatibility.md` 中的核心边界转为可重复执行的测试，避免 API 兼容性只停留在文档声明。

## 覆盖范围

- 支持的 `model`、文本 `messages`（包括 `developer` 角色）、`max_tokens`、`max_completion_tokens`、`temperature`、`stream` 和 `stream_options.include_usage`。
- Tools、`response_format`、工具调用、多模态内容和未知字段的明确拒绝。
- OpenAI 风格 `error.type`、`error.code` 和 `error.message`。
- SSE 的 `text/event-stream` Content-Type 和 `[DONE]` 结束事件。
- `stream_options` 只能在流式请求中使用，且只接受明确的 `include_usage` 布尔字段。
- `max_tokens` 与 `max_completion_tokens` 不能同时出现；前者保持兼容字段，后者保留新版 OpenAI 字段语义。
- `developer` 消息角色在 OpenAI 上游保持原样，在 Anthropic 上游转换为顶层 `system`。

## 约束

- 使用进程内固定 Provider，不访问真实网络或密钥。
- 契约测试只验证客户端可观察行为，不绑定 Provider 内部实现。
- 修改兼容边界时必须同时更新矩阵、测试和 CHANGELOG。

## 验收

```bash
make compatibility
```
