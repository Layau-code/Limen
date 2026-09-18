# Limen Provider Endpoint 绑定实施计划

## 目标

修复模型目标、Provider 地址和租户凭据之间的边界缺口：目标可以显式声明非敏感 `endpoint_id`，Router 和 Provider 都拒绝错绑，避免配置发布后把凭据或请求送到未预期的 Provider 地址。

## 语义

- `endpoint_id` 可选，格式为 `endpoint:` 加 24 位小写十六进制字符。
- ID 由 `provider.EndpointIDForBaseURL` 根据进程配置的 Provider 地址生成，不在请求中接受 URL。
- 当前每个 Provider 只支持一个进程级 endpoint；目标留空时使用该默认 endpoint。
- 配置解析校验格式，Router 激活校验目标与 Provider endpoint 的精确匹配，Provider 出站前再次校验。
- 凭据仍按 `tenant + provider + endpoint_id` 绑定；endpoint 不进入日志、Prompt、Response 或公共响应。

## 实现边界

1. 配置、目录、决策目标和 Provider 请求透传 `endpoint_id`。
2. 目标键和熔断键包含 endpoint，避免同一上游模型跨 endpoint 共享状态。
3. 进程启动时注册 OpenAI/Anthropic endpoint ID；配置发布复用 Router 校验。
4. 使用 `httptest.Server` 覆盖配置保留、错误格式、路由传播和 Provider 错绑不出站。

## 不包含

本阶段不实现动态 endpoint 注册、客户端选址、多地址负载均衡或热加载；这些能力需要独立的 endpoint 生命周期和租户级 allowlist 设计。

## 验收

- `go test ./...`、`go vet ./...`、`make check` 通过。
- 目标 endpoint ID 能进入 Provider 请求，但不进入上游 JSON。
- 错误 endpoint 在配置激活或 Provider 调用前失败，且上游调用计数为零。
- 文档、示例和凭据绑定语义保持一致。
