# Limen Provider 安全默认值计划

## 目标

保证 Provider 在任何构造路径下都不会因为缺少 HTTP Client 而退回标准库全局 Client，避免绕过 Limen 的出站安全边界。

## 规则

- 调用方注入 Client 时保留注入行为，便于使用 `httptest` 和故障注入。
- 未注入 Client 时，根据 Provider Base URL 创建带 endpoint allowlist 的安全 Client。
- 安全 Client 继续关闭环境代理和自动重定向，并校验 HTTPS 与解析后的 IP 地址。
- 生产装配仍显式注入共享安全 Client，Provider 不负责选择模型或凭据。

## 验收

- OpenAI 和 Anthropic 构造函数的 `nil` Client 不等于 `http.DefaultClient`。
- 默认 Transport 为 `secureRoundTripper`。
- Provider 普通响应、流式响应、取消、错误和 endpoint 绑定测试保持通过。
