# AIGate

AIGate 是一个使用 Go 实现、兼容 OpenAI API 协议的 AI Gateway，为内部 AI 应用和 Agent 提供统一的模型访问入口。

```text
AI Application / Agent
          |
          v
        AIGate
          |
          v
     OpenAI Provider
```

## 为什么做

- 实践 Go HTTP 服务、流式传输、并发、取消和资源管理。
- 理解模型网关的协议适配、可靠性和可观测性问题。
- 构建一个能够体现 Go 后端与 AI Infra 能力的完整工程案例。

## MVP

当前阶段只构建一条稳定、可测试的最小链路：

- `POST /v1/chat/completions`。
- OpenAI-Compatible 请求和响应。
- OpenAI Provider。
- 流式与非流式转发。
- 基础 API Key 鉴权。
- 超时、客户端取消和结构化日志。

多 Provider、动态路由、Retry/Fallback、限流、Usage 与成本统计暂不属于 MVP。

## 状态

项目处于设计阶段，尚未提供可运行服务。核心设计见 [`docs/design.md`](docs/design.md)，开发协作约束见 [`AGENTS.md`](AGENTS.md)。

## 文档维护

代码行为、配置、API 或架构发生变化时，相关文档必须在同一次改动中同步更新。

