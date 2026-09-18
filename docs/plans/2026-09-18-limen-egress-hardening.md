# Limen 出站安全边界加固计划

## 目标

让 Provider endpoint 辅助函数与配置校验共享同一安全边界，并用本地测试证明生产 HTTP Client 不会绕过 endpoint、代理和内部地址策略。

## 实施

1. Endpoint allowlist/凭据绑定拒绝用户信息、查询参数和片段。
2. 增加环境代理绕过、重定向、loopback、RFC1918、链路本地和云元数据地址测试。
3. 保持测试只连接 `httptest.Server`，不访问真实 Provider。

## 验收

- `EndpointForBaseURL` 与 `EndpointIDForBaseURL` 对非法 URL 一致拒绝。
- 即使设置 `HTTP_PROXY`/`HTTPS_PROXY`，安全 Client 仍直连 allowlist 目标。
- 内部和元数据地址在实际 Dial 前被拒绝。
