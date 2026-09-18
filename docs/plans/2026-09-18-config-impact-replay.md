# Limen 配置变更影响分析计划

## 目标

在配置发布前，把已经记录的历史 DecisionInput 应用到指定草稿，比较原计划与草稿计划，帮助确认模型、Provider、策略和 Fallback 顺序是否发生变化。

## 约束

- 只使用租户自己的 Decision Journal 和配置版本。
- 不访问 Provider，不读取当前熔断状态，不切换线上 Router。
- 保留历史请求契约、Run 快照和算法版本；只替换候选目录与配置版本。
- 目标引用和差异必须使用 opaque ID，不返回真实上游模型名。

## API

```text
POST /v1/limen/configs/{version}/replay
Scope: decisions:read, configs:read
Body: {"decision_id":"decision_..."}
```

响应包含 `original_plan`、`draft_plan`、`match` 和安全 `differences`。历史中仍存在且映射未改变的目标沿用原健康快照；新增或发生 Provider/上游映射变化的目标按关闭状态处理，避免把当前熔断状态带入发布前分析。

## 验收

- Router 重放使用草稿目标并保留历史输入，原输入不被修改。
- HTTP 接口按租户和 Scope 隔离，返回配置版本和结构化差异。
- Provider 调用计数为零，当前 Router 目录和熔断状态不变。
- 响应、日志和测试输出不包含上游模型名或 Prompt。
