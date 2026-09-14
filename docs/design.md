# Limen 第三阶段设计

## 1. 目标

在保持 OpenAI-Compatible API 的前提下，用启动时加载的模型注册表替代写死的业务路由。客户端只依赖稳定的逻辑模型名，Limen 负责选择 Provider 并替换为真实上游模型。

成功标准：

- 逻辑模型精确映射到 OpenAI 或 Anthropic 及其上游模型。
- `GET /v1/models` 返回经过鉴权、顺序稳定的模型列表。
- 未配置注册表时保留原有模型前缀行为。
- 普通响应、SSE、超时、取消和安全日志行为不回退。

## 2. 范围

当前阶段包含模型配置加载与校验、只读注册表、配置化 Router、模型列表 API，以及原有 OpenAI 和 Anthropic 转发能力。

本阶段不实现配置热加载、远程配置、第三个 Provider、模型权重、健康路由、Retry/Fallback、限流、Usage、成本统计、数据库或管理后台。

## 3. 组件边界

```text
HTTP API
  -> Bearer Key Authentication
  -> Request Normalize
  -> Model Registry + Router
  -> Provider Adapter
  -> Response / SSE Relay
```

- **HTTP API**：提供 `/v1/chat/completions` 和 `/v1/models`，共用 Limen API Key 鉴权。
- **请求标准化**：生成 `provider.ChatRequest`，拒绝暂不支持的 tools、tool calls、音频和多模态内容。
- **模型注册表**：保存客户端模型 ID、Provider 和上游模型，只在启动时构建，运行期间只读。
- **Router**：解析逻辑模型、替换上游模型并选择 Provider。
- **Provider 适配器**：只负责上游鉴权和协议转换，不感知逻辑模型映射或 HTTP Handler。
- **响应转发**：及时转发普通响应或 SSE，不聚合完整流。

## 4. 模型注册表

`LIMEN_MODELS_FILE` 指向 JSON 文件。每条模型必须包含 `id`、`provider` 和 `upstream_model`，`display_name` 可选；Provider 只能是 `openai` 或 `anthropic`，模型 ID 不能重复，模型列表不能为空。

提供配置文件时，请求模型必须精确匹配 `id`。Router 在调用 Provider 前用 `upstream_model` 替换请求模型，未注册模型返回 `400 unsupported_model`。模型列表按 ID 排序，保证 API 输出稳定。

未提供配置文件时使用兼容注册表：`gpt-*`、`o1-*`、`o3-*` 对应 OpenAI，`claude-*` 对应 Anthropic，模型名原样透传。

## 5. 请求数据流

1. 客户端携带 Limen API Key 调用聊天或模型列表接口。
2. API 层完成共用鉴权；聊天接口继续执行请求解析和最小校验。
3. Router 从只读注册表解析模型，选择 Provider 并替换真实模型名。
4. Provider 使用自己的密钥构造上游请求并完成协议转换。
5. 非流式响应直接转发；流式响应以 SSE 逐块刷新。
6. 客户端取消、服务超时或传输失败时，上游请求被取消并释放资源。

## 6. 错误处理

- Limen 鉴权失败返回 `401 invalid_api_key`。
- 请求格式错误或模型未注册返回 `400`。
- 配置文件不可读、JSON 无效、字段错误、模型为空或 ID 重复时启动失败。
- 注册表实际引用的 Provider 缺少 API Key 时启动失败。
- 上游超时映射为网关超时；已开始的流发生错误时结束连接，不执行重试。

API 错误继续使用 OpenAI 兼容结构：`{"error":{"message":"...","type":"...","code":"..."}}`。

## 7. 配置与安全

配置包含 `LIMEN_ADDR`、`LIMEN_API_KEY`、`LIMEN_MODELS_FILE`、`OPENAI_API_KEY`、`OPENAI_BASE_URL`、`ANTHROPIC_API_KEY`、`ANTHROPIC_BASE_URL` 和 `LIMEN_REQUEST_TIMEOUT`。

配置模式只要求注册表实际使用的 Provider Key；兼容模式同时使用两个 Provider，因此要求两个 Key。密钥只通过运行环境注入，不写入响应、日志或仓库。日志不得默认记录鉴权头、完整 Prompt 或 Response。

## 8. 测试策略

- 配置测试覆盖合法文件、无效 JSON、空列表、缺失字段、未知 Provider、重复 ID 和按需密钥校验。
- 注册表与 Router 测试覆盖精确映射、未知模型和四种兼容前缀。
- HTTP API 测试覆盖模型列表鉴权、排序及两种运行模式。
- 本地假 Provider 回归验证普通响应、SSE、错误、超时和取消传播，不依赖真实网络或密钥。

## 9. 文档一致性

API、配置、组件边界、运行方式或范围变化时，必须在同一次改动中同步更新 `README.md`、`AGENTS.md` 或本文档中受影响的内容。
