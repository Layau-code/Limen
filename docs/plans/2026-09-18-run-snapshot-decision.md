# Run 快照接入决策引擎

## 目标

让受治理 Chat 的真实路由与 Explain/Replay 使用同一份 Run 快照，避免预算和截止时间只在 Run 状态机中生效、却没有进入模型选择依据。

## 实现

- Router 增加带 `RunSnapshot` 的 Chat、Explain 入口，旧入口保持无状态兼容。
- HTTP Handler 在 Run 准入前读取固定策略，并把已结算金额、软预算、剩余截止时间、经济阈值和最小尝试窗口写入 DecisionInput。
- 路由配置支持 `economy_threshold_percent`（默认 20）和 `minimum_attempt_window`（默认 250ms）。
- `RemainingDeadline=0` 表示没有截止时间；有截止时间时，低于最小尝试窗口的目标返回 `deadline_insufficient`。

## 验收

- 受治理 Chat 的 Decision Journal `input.run.governed` 和预算字段真实非空。
- 接近软预算时，balanced Run 的 ExecutionPlan 切换为 economy 并记录原因码。
- 有截止时间且剩余时间不足时，不产生 Provider 调用。
- 无 Run 的兼容 Chat、历史 Replay 和 100 组 golden fixture 保持不变。
