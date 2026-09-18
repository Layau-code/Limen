# Limen 熔断策略快照计划

## 背景

Run 创建时固定配置版本和路由策略，但进程内熔断器长期存在。如果熔断器同时保存阈值和冷却时间，配置发布后同一目标会继续使用首次创建时的旧参数，导致新旧 Run 的行为无法按配置解释。

## 实现

1. 熔断器只持有失败次数、打开时间和 Half-Open 探测状态。
2. Decision 观察显式接收本次策略的冷却时间。
3. Executor 的放行、失败累计和重新打开显式接收本次策略的阈值/冷却时间。
4. `ChatOptions.Policy` 同时用于计划生成和计划执行，受治理旧 Run 不读取当前 Router 的策略参数。
5. 保留无参数辅助方法供已有单元测试使用，但生产路径不得调用隐式旧策略。

## 验收

```bash
go test ./internal/gateway -race -count=1
make check
go build -o /tmp/limen-breaker-policy ./cmd/limen
```

