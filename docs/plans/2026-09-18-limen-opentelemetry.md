# Limen OpenTelemetry 证据链实施计划

## 目标

为一次受治理请求建立可关联的 OpenTelemetry Trace：HTTP 请求、Run 准入、路由决策、每次 Provider Attempt 和最终结算使用同一 Trace。导出失败不得影响模型请求。

## 隐私边界

- 只记录稳定标识、状态、计数和有限枚举。
- 不记录 Prompt、Response、Authorization、API Key、Provider Key、租户密钥或上游模型名。
- 只传播 W3C `traceparent`，不传播可能携带任意业务数据的 Baggage。

## 实施步骤

1. 增加 Trace 传播与隐私白名单测试。
2. 增加 OTLP/HTTP 初始化和优雅关闭；未配置端点时保持无操作。
3. 增加 HTTP、准入、决策、Attempt、结算 Span。
4. 同步 README、设计与运维文档。
5. 运行格式化、静态检查、竞态测试、集成测试、构建、冒烟和基准验证。

## 验收标准

- 受治理请求的五类 Span 可通过同一 Trace ID 串联。
- Fallback 产生独立 Attempt Span，并保留结果分类。
- Trace 中不存在请求正文、响应正文和任何密钥。
- OTLP 不可用不会改变 HTTP 响应。
