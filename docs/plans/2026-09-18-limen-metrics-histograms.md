# Limen 延迟指标计划

## 目标

让 `/metrics` 能观察请求总耗时和首字节延迟，同时保持固定指标名、固定桶和低基数标签，不引入 Prometheus 客户端依赖。

## 实施

1. 在内存 Registry 中增加固定桶 Histogram，输出 Prometheus `_bucket`、`_count` 和 `_sum`。
2. 对 Histogram 复用现有标签归一化和每指标序列上限；负数、NaN 和 Inf 直接忽略。
3. Chat 请求结束时记录总耗时；`statusRecorder` 已记录首字节时再记录 TTFB，普通响应和 SSE 共用同一入口。
4. 同步 README、AGENTS、设计文档和指标测试。

## 验收

- `/metrics` 输出请求耗时和 TTFB Histogram，桶顺序和标签输出稳定。
- 非法观测值不改变计数；序列超过上限时不继续分配。
- 指标不含 Prompt、Response、Key、Request ID、租户 ID 或动态路径。
- `make check`、构建、冒烟和 PostgreSQL 集成测试通过。
