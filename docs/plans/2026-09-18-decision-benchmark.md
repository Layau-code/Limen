# Limen Decision Engine 基准计划

## 目标

为项目的核心差异化能力“可解释、可重放的能力决策”增加可重复的性能证据，避免只用 Provider 转发延迟描述网关性能。

## 范围

- 使用固定 100 个候选目标构造纯内存 `DecisionInput`。
- 基准覆盖候选硬过滤、稳定排序、计划深复制和 `input_hash`/`plan_hash` 计算。
- 不访问网络、数据库、环境密钥或真实 Provider。
- 保留现有 Router 主路径和 Fallback 基准，二者分别报告。

## 验收

- 测试确认基准输入始终包含 100 个候选目标。
- `make bench` 同时输出 Decision Engine 与 Router 基准。
- README 记录实际机器上的近期结果，并明确不构成性能承诺。
- `make check`、`make compatibility`、`make reliability`、`make build`、`make smoke` 和 `git diff --check` 通过。
