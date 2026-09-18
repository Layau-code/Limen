# Limen 流式结算完整性计划

## 背景

SSE 首字节发送后不能再切换 Provider，但响应体可能在中途读取失败或客户端写入失败。若只忽略 `io.Copy`/`Read` 错误，已经观察到的用量可能被错误标记为完整结算。

## 实现

1. `relayStream` 返回上游读取或客户端写入错误，正常 EOF 仍视为成功结束。
2. 普通响应复制同样检查 `io.Copy` 错误。
3. `Settlement.MarkIncomplete` 将已开始但未完整传输的响应标记为 `partial`；成本不再标记为可用。
4. 中断发生在响应提交之后，只影响结算证据，不触发 Fallback 或改写已经发送的 HTTP 状态。

## 验收

```bash
go test ./internal/gateway ./internal/httpapi -race -count=1
make check
go build -o /tmp/limen-stream-settlement ./cmd/limen
```

