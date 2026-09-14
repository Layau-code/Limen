# Limen

Limen（拉丁语：门槛、入口）是一个使用 Go 实现、兼容 OpenAI API 协议的 AI Gateway。

它位于 AI 应用与模型 Provider 之间，为内部 AI 应用和 Agent 提供统一、可靠、可观测的模型访问入口。

> 连接 Agent 与模型的统一入口。

```text
AI Application / Agent
          |
          v
         Limen
          |
          v
     OpenAI / Anthropic
```

## 为什么做

- 实践 Go HTTP 服务、流式传输、并发、取消和资源管理。
- 理解模型网关的协议适配、可靠性和可观测性问题。
- 构建一个能够体现 Go 后端与 AI Infra 能力的完整工程案例。

## 当前能力

当前阶段只构建一条稳定、可测试的最小链路：

- `POST /v1/chat/completions`。
- OpenAI-Compatible 请求和响应。
- OpenAI 和 Anthropic Claude Provider。
- 按模型名前缀路由：`gpt-*`、`o1-*`、`o3-*` 使用 OpenAI，`claude-*` 使用 Anthropic。
- 流式与非流式转发。
- Anthropic Messages API 到 OpenAI Chat Completions 的请求与响应转换。
- 基础 API Key 鉴权。
- 超时、客户端取消和结构化日志。

暂不支持 tools、tool calls、音频和多模态消息。Retry/Fallback、限流、Usage 与成本统计仍不属于当前阶段。

## 状态

当前第二阶段已提供可运行服务。核心设计见 [`docs/design.md`](docs/design.md)，开发协作约束见 [`AGENTS.md`](AGENTS.md)，实施计划见 [`docs/plans/2026-09-11-limen-phase2.md`](docs/plans/2026-09-11-limen-phase2.md)。

## 快速开始

要求 Go 1.24+。先设置服务端密钥并启动：

```bash
export LIMEN_API_KEY=local-limen-key
export OPENAI_API_KEY=your-openai-key
export ANTHROPIC_API_KEY=your-anthropic-key
go run ./cmd/limen
```

然后通过兼容 OpenAI 的接口发起请求：

```bash
curl http://localhost:8080/v1/chat/completions \
  -H 'Authorization: Bearer local-limen-key' \
  -H 'Content-Type: application/json' \
  -d '{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"Hello"}]}'
```

可选配置：`LIMEN_ADDR`（默认 `:8080`）、`OPENAI_BASE_URL`（默认 `https://api.openai.com/v1`）、`ANTHROPIC_BASE_URL`（默认 `https://api.anthropic.com`）和 `LIMEN_REQUEST_TIMEOUT`（默认 `60s`）。

开发检查使用 `make check`，会运行格式检查、`go vet ./...` 和竞态测试。

## 文档维护

代码行为、配置、API 或架构发生变化时，相关文档必须在同一次改动中同步更新。
