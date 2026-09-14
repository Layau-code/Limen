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
- 理解模型网关的协议适配、配置化路由、可靠性和可观测性问题。
- 构建一个能够体现 Go 后端与 AI Infra 能力的完整工程案例。

## 当前能力

- `POST /v1/chat/completions`，支持普通响应和 SSE 流式响应。
- 兼容 OpenAI 的 `GET /v1/models`。
- OpenAI 和 Anthropic Claude Provider。
- 使用逻辑模型名映射 Provider 与真实上游模型。
- Anthropic Messages API 与 OpenAI Chat Completions 的最小协议转换。
- Bearer API Key 鉴权、超时、客户端取消和安全结构化日志。

暂不支持 tools、tool calls、音频和多模态消息。Retry/Fallback、限流、Usage、成本统计和配置热加载也不属于当前阶段。

## 快速开始

要求 Go 1.24+。推荐通过模型注册表启动：

```json
{
  "models": [
    {
      "id": "smart-model",
      "provider": "anthropic",
      "upstream_model": "claude-sonnet-4-20250514",
      "display_name": "Smart Model"
    },
    {
      "id": "fast-model",
      "provider": "openai",
      "upstream_model": "gpt-4.1-mini",
      "display_name": "Fast Model"
    }
  ]
}
```

```bash
export LIMEN_API_KEY=local-limen-key
export LIMEN_MODELS_FILE=/absolute/path/to/models.json
export OPENAI_API_KEY=your-openai-key
export ANTHROPIC_API_KEY=your-anthropic-key
go run ./cmd/limen
```

只需配置注册表实际使用的 Provider Key。配置文件只在启动时加载；修改后需要重启服务。

客户端使用逻辑模型名发起请求：

```bash
curl http://localhost:8080/v1/chat/completions \
  -H 'Authorization: Bearer local-limen-key' \
  -H 'Content-Type: application/json' \
  -d '{"model":"smart-model","messages":[{"role":"user","content":"Hello"}]}'
```

查询可用模型：

```bash
curl http://localhost:8080/v1/models \
  -H 'Authorization: Bearer local-limen-key'
```

未设置 `LIMEN_MODELS_FILE` 时进入兼容模式：`gpt-*`、`o1-*`、`o3-*` 透传给 OpenAI，`claude-*` 透传给 Anthropic。兼容模式需要同时配置两个 Provider Key，`GET /v1/models` 返回这四条模式记录。

其他可选配置为 `LIMEN_ADDR`（默认 `:8080`）、`OPENAI_BASE_URL`（默认 `https://api.openai.com/v1`）、`ANTHROPIC_BASE_URL`（默认 `https://api.anthropic.com`）和 `LIMEN_REQUEST_TIMEOUT`（默认 `60s`）。

## 项目状态

第三阶段已实现模型注册表与配置化路由。核心设计见 [`docs/design.md`](docs/design.md)，开发约束见 [`AGENTS.md`](AGENTS.md)，实施计划见 [`docs/plans/2026-09-11-limen-phase3.md`](docs/plans/2026-09-11-limen-phase3.md)。

开发检查使用 `make check`，会运行格式检查、`go vet ./...` 和竞态测试。代码行为、配置、API 或架构变化时，相关文档必须在同一次改动中同步更新。
