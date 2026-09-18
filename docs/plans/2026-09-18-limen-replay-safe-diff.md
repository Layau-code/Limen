# Limen Replay 安全差异实施计划

## 目标

让 Replay 在不暴露真实上游模型、endpoint 或凭据的前提下，识别“同一 target ID 但隐藏映射已经变化”的配置影响。

## 规则

- 目标顺序、逻辑模型、Provider 和 opaque target ID 继续使用公开安全视图。
- Provider、`upstream_model` 或 `endpoint_id` 变化时，增加 `targets[i]/mapping` 差异。
- 能力、流式支持、质量/成本等级、上下文窗口、数据等级或价格变化时，增加 `targets[i]/policy` 差异。
- 映射差异只返回 `path` 和 `kind=changed`，不返回原值或新值。
- 策略差异同样不返回价格或内部配置原值。
- `plan_hash` 仍作为完整快照变化的最终证据；Replay 不访问 Provider。

## 验收

- 同一 target ID 更换上游模型或 endpoint 时能返回 mapping 差异。
- 差异 JSON 不包含任意上游模型名、endpoint ID 或密钥。
- Provider 变化仍保持现有安全差异测试通过。
