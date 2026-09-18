# OpenAI SSE 完整性计划

## 背景

OpenAI Provider 原先只观察 SSE 用量并原样转发，未验证 `[DONE]` 或错误事件。上游连接截断时，客户端可能收到正常 EOF，无法区分完整响应和部分响应。

## 实施

1. 增加缺少 `[DONE]` 与上游错误事件的观察器测试，并验证错误信息不包含 Provider 正文。
2. 让观察器在完整终止事件外返回稳定读取错误，同时保留首段实时转发和正常 Usage 采集。
3. 同步 OpenAI 兼容矩阵、工程规范、架构设计、最终设计、README 和变更记录。

## 验收

- 完整 OpenAI SSE 的字节内容和 Usage 不变。
- 截断流或错误事件不会伪装成成功 EOF，也不会触发已开始响应的 Fallback。
- `make reliability`、`make check` 和构建通过。
