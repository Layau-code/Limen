# AIGate MVP 设计

## 1. 目标

用最小范围验证一个生产思维的 AI Gateway 请求闭环：上游通过 OpenAI-Compatible API 发起请求，AIGate 完成鉴权并将请求转发到 OpenAI，同时正确处理普通响应、SSE 流、超时和客户端断开。

成功标准：

- 上游可以使用 OpenAI SDK 并仅修改 `base_url` 接入。
- 流式与非流式请求都能完成端到端转发。
- 客户端断开或超时后，上游 Provider 请求被取消。
- 密钥不出现在响应和日志中。
- 核心正常路径、错误路径和取消路径可自动验证。

## 2. 范围

MVP 包含 API 层、鉴权、OpenAI 转发和基础日志。只支持 `POST /v1/chat/completions`，只接入 OpenAI。

MVP 明确不解决多 Provider、动态路由、重试与故障转移、限流、计费、持久化和完整监控平台。这些能力不应以“预留框架”的形式增加当前复杂度。

## 3. 组件边界

```text
HTTP API
  -> Authentication
  -> Chat Completion Service
  -> OpenAI Client
  -> Response / SSE Relay
```

- **HTTP API**：解析请求、校验基本格式、映射响应与错误。
- **Authentication**：验证 AIGate API Key，不负责用户体系或权限管理。
- **Chat Completion Service**：协调一次请求，不包含 HTTP 框架细节。
- **OpenAI Client**：构造上游请求并管理连接、超时和响应体。
- **Response / SSE Relay**：将上游响应及时转发给客户端，不解析或聚合完整流。

只有出现第二个真实 Provider 后，才根据重复点提取稳定的 Provider 接口。

## 4. 请求数据流

1. 客户端携带 AIGate API Key 调用 `/v1/chat/completions`。
2. API 层完成鉴权和最小请求校验。
3. 服务层将请求上下文传给 OpenAI Client。
4. OpenAI Client 使用服务端保存的 OpenAI Key 请求 Provider。
5. 非流式响应直接转发；流式响应以 SSE 逐块刷新。
6. 客户端取消、服务超时或传输失败时，取消上游请求并释放资源。
7. 请求结束后记录不含敏感正文的结构化日志。

## 5. 错误处理

- AIGate 鉴权失败返回 `401`。
- 请求格式错误返回 `400`。
- 上游超时映射为网关超时错误。
- 上游在响应开始前失败时，尽可能保留有用状态与错误信息。
- 流式响应开始后发生错误时结束连接并记录原因，不尝试 Retry 或 Fallback。
- 客户端断开属于取消事件，不记录为服务端故障。

具体错误响应结构在实现计划中依据 OpenAI 兼容性测试确定。

## 6. 配置与安全

首版配置至少包含监听地址、AIGate API Key、OpenAI API Key、OpenAI Base URL 和请求超时。敏感配置仅通过运行环境注入，不提交到仓库。

日志允许记录请求 ID、路径、模型、状态、总延迟和取消原因；不得记录鉴权头、Provider Key 或默认记录完整 Prompt/Response。

## 7. 测试策略

- 单元测试覆盖鉴权、请求校验和错误映射。
- 使用本地假 Provider 验证普通转发、SSE 分块、上游错误和超时。
- 验证客户端取消会传播到假 Provider。
- 使用竞态检测验证并发路径。
- 不在默认测试中依赖真实 OpenAI 网络或真实密钥。

## 8. 后续演进

MVP 稳定后，再按实际需求依次评估：Provider 抽象与第二 Provider、逻辑模型路由、Usage 统计、Retry/Fallback、限流、成本治理和完整可观测性。每项能力单独设计，不预先承诺技术组件。

## 9. 文档一致性

API、配置、组件边界、运行方式或范围变化时，必须同步更新 `README.md`、`AGENTS.md` 或本文档中受影响的内容。
