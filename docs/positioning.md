# Limen 项目定位与差异化

## 一句话定位

Limen 是一个以 Agent Run 为治理边界、以能力契约驱动模型选择、并能解释和重放每次路由决策的 OpenAI 兼容网关。

## 能力边界

| 方向 | 常见网关能力 | Limen 的重点 | 可验证证据 |
| --- | --- | --- | --- |
| Provider 接入 | 多 Provider、协议转换 | OpenAI 与 Anthropic 的文本 Chat、普通响应和 SSE | `make compatibility` |
| 故障处理 | 重试、Fallback、熔断 | 先生成不可变 ExecutionPlan；只对可重试瞬时错误切换，SSE 开始后不重放 | `make reliability` |
| 模型路由 | 按模型名或权重路由 | 客户端声明能力、数据等级、上下文和策略，Decision Engine 生成稳定排序与原因码 | `make demo`、`make bench` |
| Agent 治理 | 单请求超时或限额 | Run 级软预算、并发准入、幂等、租约、取消和诚实的未知费用状态 | `make integration` |
| 可解释性 | 日志和指标 | Decision Journal、`input_hash`/`plan_hash`、Explain、Replay 和配置影响分析 | `limen explain`、Replay API |
| 发布安全 | 配置文件或管理 API | 不可变配置版本、endpoint 绑定、离线 validate/diff、审批和草稿预演 | `make validate`、`limen diff` |
| 安全边界 | Bearer Key 和日志脱敏 | 租户 Principal、Scope、RLS、凭据隔离、安全出站 Client 和稳定错误视图 | PostgreSQL 集成测试 |

## 真正的差异化

单独的多 Provider、Fallback 或成本统计并不特殊。Limen 的差异来自它们被串成一条可验证证据链：

```text
能力契约
  → 确定性 DecisionInput
  → 可重放 ExecutionPlan
  → 能力安全的 Provider 执行
  → Run 级结算与审计
```

因此一次模型调用不仅得到响应，还能回答：

- 为什么选择这个目标？
- 为什么其他目标没有进入计划？
- 配置发布前，已有决策会受到什么影响？
- Provider 已经收费但进程崩溃时，账本如何保持诚实？
- 同一个决策快照在算法升级后是否仍能复现？

## 与成熟网关的对标方式

Limen 当前适合对标的是路由可靠性、审计性、配置安全和 Agent 治理的工程设计，不宣称在 Provider 数量、插件生态、吞吐规模或 OpenAI 全量协议兼容上领先成熟网关。

项目刻意后置以下能力：第三 Provider、Responses/Tools/多模态、分布式熔断、每日额度、动态权重、Retry/Fallback DSL 和大型管理后台。这样可以让当前能力边界真实、可测试、可解释。

## 简历表述

> 设计并实现一个面向 Agent Run 的 Go AI Gateway：通过能力契约和确定性 Decision Engine 生成可重放执行计划，结合安全 Fallback、租户隔离、事务化结算和 Decision Journal，解决多模型调用中的路由可解释性、故障恢复和成本不确定性问题。

## 最小演示路径

```bash
make demo          # 离线展示能力契约选模、Fallback、Replay 与 Run 结算
make validate      # 离线验证模型目录和出站 endpoint 绑定
make compatibility # 验证 OpenAI Chat 兼容边界
make reliability   # 验证取消、超时、Fallback 和流式不重放
make integration   # 验证 PostgreSQL、RLS、幂等和崩溃恢复
make bench         # 测量决策和路由热路径
```

所有演示和测试均不需要真实 Provider Key，也不会输出 Prompt、Response 或凭据。
