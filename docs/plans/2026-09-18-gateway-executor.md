# Limen Gateway Executor 边界重构

## 目标

修正 Router 同时承担目录解析、决策和 Provider 执行的问题，让执行层只消费已经生成的 `ExecutionPlan`，从结构上保证 Fallback 不会重新选择模型。

## 实现

- 新增 `internal/gateway/executor.go` 的 `Executor`。
- Router 保留注册表、DecisionInput 和熔断快照职责；`executePlan` 仅转发到 Executor。
- Executor 接收 Provider、策略和熔断状态读取边界，负责总预算、单次超时、Fallback、响应关闭和 Attempt 报告。
- 保持既有 `Router.Chat*`、SSE、取消和熔断行为不变。

## 验证

复用现有 Router Fallback、超时、取消、流式和竞态测试；执行层不读取 `ModelRegistry`，计划中的上游模型只在执行入口替换一次。
