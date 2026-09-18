# 真实二进制冒烟契约

## 目标

让冒烟脚本证明构建出的 Limen 二进制能够完成最小安全启动，而不依赖真实 Provider、数据库或业务密钥。

## 覆盖范围

- `/livez` 可响应，`/readyz` 在监听成功后返回成功。
- 未鉴权的 `/v1/models` 被拒绝，带占位 Bearer Key 时返回 OpenAI 风格列表。
- 未注册模型的 Chat 请求返回 `unsupported_model`，且错误不回显请求中的模型名。

## 边界

脚本只使用临时二进制、临时目录和占位密钥；不调用真实 Provider，不把冒烟结果当作 Provider 协议集成测试。真实 Provider 行为由 `httptest` 测试和 `make reliability` 覆盖。
