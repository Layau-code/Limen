# Limen 计划摘要响应头

## 目标

让客户端可以从一次 Chat 或 Dry Run 响应直接关联到可查询、可 Replay 的 Decision Journal 计划。

## 约束

- 新增 `X-Limen-Plan-Hash`，只包含规范 ExecutionPlan 的 SHA-256 摘要。
- 成功、Dry Run 和能力筛选失败都尽可能返回计划摘要。
- 日志和 Trace 只记录该固定摘要，不记录 Prompt、Response、密钥或真实上游模型名。
- 不把摘要当作租户标识、请求正文或账本事实；详细证据仍通过 `X-Limen-Decision-ID` 查询。

## 验收

- Chat 成功响应的 Header 与 Decision Plan 的 `plan_hash` 一致。
- Dry Run 响应的 Header 与 JSON 中的 `plan_hash` 一致。
- `no_eligible_target` 错误仍返回非空计划摘要。
- 日志和 Trace 只通过固定白名单接收该 Header。
