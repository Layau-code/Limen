# Limen Metrics 语义与隐私加固计划

## 目标

让 `/metrics` 只暴露低基数、可解释且与真实执行一致的计数器，不允许被拒绝的用户输入进入 Label。

## 改动

1. 请求模型只记录已注册逻辑 ID、`auto` 或兼容模式名；其他值归并为 `unsupported`。
2. 请求状态归并为 `2xx`、`4xx`、`5xx`，拒绝原因只使用 Limen 稳定错误码。
3. Attempt 指标只读取真实 Provider 调用报告，记录目标 ID、Provider、状态类别和结果；熔断跳过不计为 Attempt。
4. 保持固定三类计数器、序列数量上限和 `admin` Scope，不增加请求、Run、租户或原始错误 Label。

## 验收

- 未注册模型哨兵字符串不会出现在 Metrics。
- Fallback 的每次真实调用各产生一个目标序列。
- `circuit_open` 等未调用步骤不会增加 Attempt 指标。
- 格式化、静态检查、竞态测试、集成测试、构建和冒烟通过。
