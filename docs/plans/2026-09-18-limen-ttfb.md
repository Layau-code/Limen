# Limen 首字节延迟观测计划

## 目标

让普通响应和 SSE 都能记录客户端首次收到响应正文的时间，区分 Provider 首字节慢与完整响应慢。

## 实施

1. 在现有 `statusRecorder` 第一次 `Write` 时记录时间戳。
2. 结构化日志增加 `ttfb_ms`；HTTP 根 Trace 增加 `limen.ttfb_ms`。
3. 没有响应正文时省略字段，避免把 Header/状态误报为首字节。

## 验收

- 普通响应首字节和 SSE 首段都能被观测。
- TTFB 不包含 Prompt、Response 或密钥。
- 既有流式 Flush、响应状态和取消语义不变。
