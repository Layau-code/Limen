# Limen

Limen（拉丁语：门槛、入口）是一个面向 Agent 的 Go AI Gateway。它提供 OpenAI 兼容接口，把稳定的逻辑模型名路由到 OpenAI 或 Anthropic，并在瞬时故障时按顺序切换备用目标。

```text
Agent / 应用 → Limen API Key → 模型注册表 → 预算感知路由 → Provider → 普通响应 / SSE
```

## 项目亮点

- 逻辑模型名与真实上游模型解耦，配置只在启动时加载并保持只读。
- 一次请求共享总时间预算；每个目标最多调用一次，避免重试风暴和重复计费。
- 仅对明确的瞬时状态和传输错误执行 Fallback；SSE 开始后不重放。
- 进程内并发安全熔断器、可解释路由响应头和不记录敏感正文的结构化日志。
- Provider 用量采集与按目标价格的定点成本结算；普通响应和 SSE 都保持实时转发。
- 只使用 Go 标准库，包含竞态测试、真实二进制冒烟测试、Docker 和 CI 资产。

## 快速开始

要求 Go 1.24+。先准备 `configs/models.example.json` 格式的模型文件：

```json
{
  "routing": {"attempt_timeout": "10s", "failure_threshold": 3, "cooldown": "30s"},
  "models": [{
    "id": "smart-model",
    "display_name": "Smart Model",
    "targets": [
      {
        "provider": "openai",
        "upstream_model": "gpt-5-mini",
        "pricing": {"input_per_million_usd": "0.250000", "output_per_million_usd": "2.000000"}
      },
      {
        "provider": "anthropic",
        "upstream_model": "claude-sonnet-4-20250514",
        "pricing": {"input_per_million_usd": "3.000000", "output_per_million_usd": "15.000000"}
      }
    ]
  }]
}
```

使用二进制启动：

```bash
make build
export LIMEN_API_KEY=local-limen-key
export LIMEN_MODELS_FILE=/absolute/path/to/models.json
export OPENAI_API_KEY=your-openai-key
export ANTHROPIC_API_KEY=your-anthropic-key
./bin/limen
```

也可以使用 Docker：

```bash
docker build -t limen:dev .
docker run --rm -p 8080:8080 \
  -e LIMEN_API_KEY=local-limen-key \
  -e OPENAI_API_KEY=your-openai-key \
  -e ANTHROPIC_API_KEY=your-anthropic-key \
  -e LIMEN_MODELS_FILE=/etc/limen/models.json \
  -v "$PWD/configs/models.example.json:/etc/limen/models.json:ro" limen:dev
```

调用时使用逻辑模型名：

```bash
curl http://localhost:8080/v1/chat/completions \
  -H 'Authorization: Bearer local-limen-key' \
  -H 'Content-Type: application/json' \
  -d '{"model":"smart-model","messages":[{"role":"user","content":"Hello"}]}'
```

`GET /v1/models` 与聊天接口共用 Bearer Key 鉴权。配置模式的 `owned_by` 为 `limen`；未配置模型文件时进入兼容模式，支持 `gpt-*`、`o1-*`、`o3-*` 和 `claude-*`，并要求两个 Provider Key。

成功或最终上游响应会带有以下安全摘要：

```text
X-Limen-Provider: anthropic
X-Limen-Attempts: 2
X-Limen-Route: openai:503>anthropic:200
```

如果目标配置了 `pricing`，响应结束后还会通过 HTTP Trailer 和结构化日志提供 `input_tokens`、`output_tokens`、`total_tokens`、`cost_usd` 和 `settlement_status`。SSE 内容仍然逐块推送，不会等待完整响应；如果上游没有返回用量或客户端提前断开，费用字段会留空。

价格字段使用每百万 Token 的美元字符串，输入价和输出价必须同时填写。当前版本只负责请求结束后的结算，不实现每日额度或超额拦截。

## 配置与运维

环境变量包括 `LIMEN_ADDR`（默认 `:8080`）、`LIMEN_API_KEY`、`LIMEN_MODELS_FILE`、`OPENAI_API_KEY`、`OPENAI_BASE_URL`、`ANTHROPIC_API_KEY`、`ANTHROPIC_BASE_URL`、`LIMEN_REQUEST_TIMEOUT`（默认 `60s`）和可选的 `LIMEN_HEALTH_URL`。模型文件修改后需重启；只校验注册表实际引用的 Provider Key。

`/livez` 表示进程存活，`/readyz` 表示已完成启动；`limen version` 输出版本信息，`limen healthcheck` 检查本地就绪状态。更多关闭流程、日志和排障说明见 [`docs/operations.md`](docs/operations.md)。

## 开发验证

```bash
make check   # gofmt、go vet、竞态测试
make smoke   # 真实二进制启动与 API 冒烟
make bench   # Router 主路径与 Fallback 基准
```

本机 Apple M5、darwin/arm64 的一次基准结果为：主路径约 `461.5 ns/op`、17 次分配；Fallback 路径约 `587.2 ns/op`、20 次分配。该数字只用于描述测量环境，不构成性能承诺。

设计决策见 [`docs/design.md`](docs/design.md)，开发规范见 [`AGENTS.md`](AGENTS.md)，变更记录见 [`CHANGELOG.md`](CHANGELOG.md)。
