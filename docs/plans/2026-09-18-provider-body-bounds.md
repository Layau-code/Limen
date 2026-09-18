# Limen Provider 响应边界计划

## 目标

统一 Provider 响应的内存安全边界，避免协议转换或用量观察因异常上游响应无界缓存。

## 实施

- 保留现有普通响应和 SSE 的有界用量观察。
- Anthropic 普通 JSON 响应最多读取 4 MiB，超过上限时在转换前返回本地请求错误。
- 解析器拒绝多个 JSON 值，且不把上游响应正文写入错误日志。
- 使用 `httptest.Server` 覆盖超大响应，验证错误路径；转换函数仍通过统一关闭逻辑释放上游响应体。

## 验收

- 超过 4 MiB 的 Anthropic 普通响应不会返回成功结果。
- 正常普通响应、SSE、用量采集和客户端取消回归通过。
- `make check`、`make compatibility`、`make reliability`、`make integration`、`make build`、`make smoke` 和 `git diff --check` 通过。
