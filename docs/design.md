# Limen 第二阶段设计

## 1. 目标

在保持 OpenAI-Compatible API 不变的前提下，验证 Limen 对多个 Provider 的统一适配：上游通过统一接口发起请求，Limen 按模型名前缀选择 OpenAI 或 Anthropic，并正确处理普通响应、SSE 流、超时和客户端断开。

成功标准：

- 上游可以使用 OpenAI SDK 并仅修改 `base_url` 接入 OpenAI 或 Claude 模型。
- 流式与非流式请求都能完成端到端转发。
- 客户端断开或超时后，上游 Provider 请求被取消。
- 密钥不出现在响应和日志中。
- 核心正常路径、错误路径和取消路径可自动验证。

## 2. 范围

当前阶段包含 API 层、鉴权、Provider 路由、OpenAI 转发、Anthropic Messages API 转换和基础日志。只支持 `POST /v1/chat/completions`，请求体上限为 4 MiB。

当前阶段明确不解决第三个 Provider、重试与故障转移、限流、计费、持久化和完整监控平台。这些能力不应以“预留框架”的形式增加当前复杂度。

## 3. 组件边界

```text
HTTP API
  -> Authentication
  -> Request Normalize
  -> Model Router
  -> Provider Adapter
  -> Response / SSE Relay
```

- **HTTP API 层**：解析请求、校验基本格式、映射响应与错误。
- **鉴权模块**：验证 Limen API Key，不负责用户体系或权限管理。
- **请求标准化**：将 OpenAI 请求转换为 `provider.ChatRequest`，拒绝暂不支持的 tools、tool calls、音频和多模态内容。
- **模型路由**：`gpt-*`、`o1-*`、`o3-*` 选择 OpenAI，`claude-*` 选择 Anthropic。
- **Provider 适配器**：各自管理上游鉴权、请求构造、响应转换、超时和响应体。
- **响应与 SSE 转发模块**：将上游响应及时转发给客户端，不解析或聚合完整流。

Provider 接口只包含统一聊天请求和统一响应，不暴露 HTTP Handler；新增 Provider 必须通过适配器完成协议转换。

## 4. 请求数据流

1. 客户端携带 Limen API Key 调用 `/v1/chat/completions`。
2. API 层完成鉴权和最小请求校验。
3. API 层将标准化请求交给 Model Router。
4. Router 按模型名前缀选择 Provider，Provider 使用自己的服务端密钥请求上游。
5. 非流式响应直接转发；流式响应以 SSE 逐块刷新。
6. 客户端取消、服务超时或传输失败时，取消上游请求并释放资源。
7. 请求结束后记录不含敏感正文的结构化日志。

## 5. 错误处理

- Limen 鉴权失败返回 `401`。
- 请求格式错误返回 `400`。
- 上游超时映射为网关超时错误。
- 上游在响应开始前失败时，尽可能保留有用状态与错误信息。
- 流式响应开始后发生错误时结束连接并记录原因，不尝试 Retry 或 Fallback。
- 客户端断开属于取消事件，不记录为服务端故障。

错误响应使用兼容 OpenAI 的结构：`{"error":{"message":"...","type":"...","code":"..."}}`。

## 6. 配置与安全

配置包含 `LIMEN_ADDR`、`LIMEN_API_KEY`、`OPENAI_API_KEY`、`OPENAI_BASE_URL`、`ANTHROPIC_API_KEY`、`ANTHROPIC_BASE_URL` 和 `LIMEN_REQUEST_TIMEOUT`。服务使用 `net/http` 和 `log/slog`；敏感配置仅通过运行环境注入，不提交到仓库。

日志允许记录请求 ID、路径、模型、状态、总延迟和取消原因；不得记录鉴权头、Provider Key 或默认记录完整 Prompt/Response。

## 7. 测试策略

- 单元测试覆盖鉴权、请求标准化、模型路由和错误映射。
- 使用本地假 Provider 验证普通转发、SSE 分块、上游错误和超时。
- 使用本地假 Anthropic 服务验证 system 提取、消息转换和 Anthropic SSE 事件转换。
- 验证客户端取消会传播到假 Provider。
- 使用竞态检测验证并发路径。
- 不在默认测试中依赖真实 OpenAI 网络或真实密钥。

## 8. 后续演进

第二阶段稳定后，再按实际需求依次评估：第三个 Provider、Usage 统计、Retry/Fallback、限流、成本治理和完整可观测性。每项能力单独设计，不预先承诺技术组件。

## 9. 文档一致性

API、配置、组件边界、运行方式或范围变化时，必须同步更新 `README.md`、`AGENTS.md` 或本文档中受影响的内容。
