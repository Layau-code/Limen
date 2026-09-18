# Limen 第二阶段实施计划

## 目标

引入最小 Provider 接口，按模型名前缀在 OpenAI 与 Anthropic Claude 之间路由，并将 Anthropic Messages API 的普通响应和 SSE 转换为 OpenAI Chat Completions 格式。

## 边界

- 保持 `POST /v1/chat/completions` 不变。
- 支持文本 system、developer、user、assistant 消息，以及 `max_tokens`、`temperature` 和 `stream`。
- 暂不支持 tools、tool calls、音频、多模态、Retry/Fallback、限流、Usage 和成本统计。
- Provider 只接收标准化请求，不依赖 HTTP Handler。

## 路由与配置

- `gpt-*`、`o1-*`、`o3-*` 路由到 OpenAI。
- `claude-*` 路由到 Anthropic。
- 新增 `ANTHROPIC_API_KEY` 和 `ANTHROPIC_BASE_URL`，默认地址为 `https://api.anthropic.com`。

## 测试与验收

- 使用 `httptest.Server` 验证两个 Provider 的请求头和请求体。
- 验证普通 JSON、SSE 文本增量、完成原因、上游错误、超时和取消传播。
- 运行 `make check` 和 `go build ./cmd/limen`。
