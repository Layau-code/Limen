# Provider 错误响应体关闭计划

## 背景

Provider 的正常实现会在返回错误时返回空响应，但统一接口不能把这个约定当成资源安全的唯一防线。若适配器异常地同时返回 `Response` 和 `error`，Executor 若忽略 `Response.Body`，可能泄漏上游连接。

## 实施

1. 增加 Router 回归测试，构造“非空 Body + Provider 错误”，确认响应体被关闭。
2. 在 Gateway Executor 的错误分支防御性关闭非空 Body，再执行错误分类和 Fallback。
3. 在 Provider 接口注释、工程规范、设计文档和 README 中固定该资源边界。

## 验收

- Provider 错误附带响应体时不会泄漏连接。
- 正常响应、Fallback、超时、取消和流式不重放行为不变。
- `make reliability`、`make check` 和构建通过。
