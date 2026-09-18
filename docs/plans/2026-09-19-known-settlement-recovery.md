# 已知结算的租约恢复

## 问题

实例可能在已知费用写入 `settlement_jobs` 后崩溃。租约扫描会把请求标记为 `abandoned`，并为释放并发名额减少 Run 的 `in_flight`。如果后台任务随后仍按普通请求结算，就会再次减少 `in_flight`，或者因为状态不再是 `settlement_pending` 而永远无法写入账本。

## 方案

- `RequestAbandoned` 且任务携带确定费用时，使用恢复结算路径写入唯一 Ledger。
- 恢复结算只累计费用，不重复释放并发名额。
- 结算后按软预算、截止时间、完成标记和其他待结算请求恢复 Run；仍有未知费用时继续 `suspended_accounting`。
- 普通 `settlement_pending` 请求继续使用原有路径，未知费用仍交给管理员处置。

## 验收

- Memory Store 覆盖“租约过期 → 已知任务处理 → Run 恢复”。
- PostgreSQL 集成测试覆盖同一流程和唯一账本。
- 重复处理不会增加账本条目或改变已结算金额。

