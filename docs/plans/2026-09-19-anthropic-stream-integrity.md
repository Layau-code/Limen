# Anthropic SSE 完整性计划

## 背景

流式响应一旦开始就不能重放，也不能把截断或上游错误伪装成成功。因此，Anthropic Messages SSE 必须把错误事件和未收到终止事件的连接关闭传递到读取方。

## 实施

1. 增加上游 `error` 事件、缺少 `message_stop` 和 CRLF 分隔的 Provider 测试。
2. 转换器对 `error` 事件返回稳定读取错误，并要求正常结束经过 `message_stop`。
3. 同步 OpenAI 兼容矩阵、工程规范、架构设计、最终设计、README 和变更记录。

## 验收

- 完整 Anthropic 流仍输出 OpenAI SSE 和 `[DONE]`。
- 上游错误或截断流不会输出伪造的 `[DONE]`，读取方能观察到错误。
- `make reliability`、`make check` 和构建通过。
