# Limen RC 发布门禁设计

## 目标

在创建 `v1.0.0-rc1` 前，用一套可重复的门禁证明 Limen 的离线可靠性和真实 Provider 联调都成立。门禁不扩大 1.0 的协议范围，也不把真实密钥放进仓库、CI 或日志。

## 两层门禁

### `make release-check`

这是默认的、可在 CI 执行的离线门禁，包含：

- 格式、生产方法注释、`go vet`、Replay fixture 和竞态测试；
- OpenAI 兼容契约、Fallback/取消/超时/流式不重放等可靠性测试；
- 临时 PostgreSQL 集成测试，验证 RLS、幂等、租约、结算和取消；
- 构建、真实二进制冒烟、配置预检、离线演示和基准；
- 工作区空白差异检查。

Provider 可靠性在这一层使用 `httptest` 和故障注入，不访问真实网络。Fallback 的瞬时失败语义因此可以稳定复现。

### `make release-live`

这是发布候选版本前手动执行的真实联调门禁。脚本只有在显式设置 `LIMEN_LIVE_TEST=1` 时才会运行，并要求同时提供 OpenAI 和 Anthropic 的密钥及测试模型名。脚本会启动临时 Limen 进程，验证：

- OpenAI 普通响应和 SSE；
- Anthropic 普通响应和 SSE；
- Bearer 鉴权和逻辑模型到真实上游模型的映射。

请求使用固定的最小文本和低输出上限，响应正文只写入临时文件并在退出时删除；脚本只输出检查名称，不输出密钥、Prompt、完整 Response、上游模型名或 Provider 错误正文。

真实 Provider 的瞬时失败不会被脚本主动制造，以免依赖供应商故障或产生额外费用。Fallback 证据由 `release-check` 的故障注入测试提供；真实联调只证明协议、凭据和端到端转发成立。

## 真实联调环境变量

必填：

```text
LIMEN_LIVE_TEST=1
OPENAI_API_KEY
ANTHROPIC_API_KEY
LIMEN_LIVE_OPENAI_MODEL
LIMEN_LIVE_ANTHROPIC_MODEL
```

可选：

```text
OPENAI_BASE_URL                 # 默认 https://api.openai.com/v1
ANTHROPIC_BASE_URL              # 默认 https://api.anthropic.com
LIMEN_LIVE_PORT                 # 默认使用临时本地端口
LIMEN_LIVE_REQUEST_TIMEOUT      # 默认 30s
```

`OPENAI_BASE_URL` 和 `ANTHROPIC_BASE_URL` 仍受 Limen 的 HTTPS 和出站 endpoint 校验。Key 只能通过环境变量或外部 Secret 注入，不能写入模型文件或命令行参数。

## 发布证据

发布记录至少应保存：

1. `make release-check` 的成功输出和提交 SHA；
2. `make release-live` 的成功摘要、执行时间和测试模型的公开名称；
3. 尚未覆盖的规模、成本、多地域和第三 Provider 能力说明。

真实联调不是容量测试，也不是费用上限保证。若 Provider 账单、网络策略或模型权限发生变化，应重新执行联调并保留新的证据。
