# Run 幂等重试 HTTP 契约

## 目标

让客户端在网络超时后可以安全重试受治理 Chat，同时明确知道请求仍在执行、已经完成，还是请求正文与首次请求冲突。

## 语义

- 同一租户、接口和 `Idempotency-Key` 只允许一次 Provider 执行。
- 首次执行期间重试返回 `409 request_in_progress`、原 `X-Limen-Request-ID` 和固定 `Retry-After: 1`。
- 首次请求完成后重试返回 `409 request_already_processed`；客户端通过 Request 查询接口读取结果和结算状态。
- 相同幂等键绑定不同规范请求哈希时返回 `409 idempotency_conflict`。
- 任意重复分支都不能创建新的 Request、Attempt、Ledger 或 Provider 调用。

## 验收

- 并发重试只产生一个 Provider 调用。
- 进行中、已完成和哈希冲突响应都返回原 Request ID。
- 只有进行中响应返回 `Retry-After: 1`。
- Memory Store 和 PostgreSQL 唯一约束保持相同语义。
