# Limen v0.2 用量与成本结算设计

## 1. 状态与目标

本文档记录已经确认、尚待实施的 Limen v0.2 设计。实现完成前，当前可用能力仍以 `README.md` 和 `docs/design.md` 为准。

本阶段在不改变 OpenAI 兼容接口、可靠性路由和实时 SSE 行为的前提下，为每次请求采集实际 Token 用量并计算成本。结算在请求结束后完成，不对单个请求设置金额上限，也不会中断已经开始的响应。

项目后续可以基于结算结果实现用户每日额度：最后一个请求完成后记账，即使它使当日累计金额超过额度也正常返回；超额状态只拦截后续请求。本阶段仅建立可信的结算基础，不实现用户、额度、持久化或拦截。

## 2. 设计原则

1. 普通响应和 SSE 都必须继续转发；结算失败不能破坏模型响应。
2. SSE 边接收边转换、边发送，只保留当前事件所需的有界数据。
3. Provider 负责理解上游用量协议，Gateway 负责汇总尝试并计算成本。
4. 一次客户端请求产生的所有上游尝试都属于同一次结算；只统计上游明确报告的用量，不估算未知费用。
5. 金额使用十进制定点整数计算，不使用二进制浮点数。
6. 缺失价格或用量时省略费用，不把未知值写成零。
7. 结构化日志是结算记录的主要来源；HTTP Trailer 只提供客户端可见的便利信息。
8. 不记录 API Key、Prompt、响应正文或上游原始事件。

## 3. 计价配置

价格属于具体目标，因为同一逻辑模型的主目标和备用目标可能使用不同 Provider、上游模型或价格。

```json
{
  "routing": {
    "attempt_timeout": "10s",
    "failure_threshold": 3,
    "cooldown": "30s"
  },
  "models": [
    {
      "id": "smart-model",
      "display_name": "Smart Model",
      "targets": [
        {
          "provider": "openai",
          "upstream_model": "gpt-5-mini",
          "pricing": {
            "input_per_million_usd": "0.250000",
            "output_per_million_usd": "2.000000"
          }
        },
        {
          "provider": "anthropic",
          "upstream_model": "claude-sonnet-4-20250514"
        }
      ]
    }
  ]
}
```

配置规则如下：

- `pricing` 整体可选；省略时仍采集 Token，但不计算该目标费用。
- 一旦提供 `pricing`，`input_per_million_usd` 和 `output_per_million_usd` 必须同时存在。
- 价格使用 JSON 字符串，单位固定为每百万 Token 的美元价格。
- 价格必须是非负普通十进制数，不接受指数形式，最多保留九位小数。
- 非法格式、字段缺失、负数、精度超限或无法安全表示的数值使启动失败。
- 模型文件继续严格拒绝未知字段。
- 兼容模式没有目标价格，因此只输出上游报告的 Token，费用保持空白。

内部将美元金额转换为十亿分之一美元的整数进行计算。Token 数量与每百万 Token 价格相乘后除以一百万，余数使用四舍五入；乘法和加法必须检查溢出。对外 `cost_usd` 最多输出九位小数，可以移除无意义的末尾零，但不能使用科学计数法。

## 4. 组件边界

```text
HTTP API
  -> Router 创建请求级结算记录
  -> 依次调用目标
       -> Provider 转换协议并采集该次尝试的 Usage
       -> Router 记录目标、状态和 Usage
  -> HTTP API 实时转发普通响应或 SSE
  -> 响应结束后汇总 Settlement
  -> 写入结构化日志和 HTTP Trailer
```

- `internal/config`：解析并校验可选计价配置，将价格转换为定点整数。
- `internal/provider`：定义统一 `Usage`，从 OpenAI 或 Anthropic 的普通响应和 SSE 中采集用量；不读取目标价格，也不计算美元费用。
- `internal/gateway`：把每次真实上游调用记录为一次 attempt，关联目标价格，汇总本次请求的 Token、成本和完整性。
- `internal/httpapi`：在转发前声明 Trailer，持续写出响应，在结束后发布结算字段并记录安全日志。

不新增通用账单服务、存储接口或 Provider 插件系统。只有出现每日额度和持久化需求时，才从当前结算结果提取账本边界。

## 5. 用量模型与完整性

Provider 统一报告：

```text
input_tokens
output_tokens
```

`total_tokens` 始终由 Limen 对两个非负字段求和，不信任上游可能不一致的总数。Provider 没有完整获得两个字段时，不构造零值用量。

请求级结算状态分为：

- `complete`：所有实际发出的尝试都得到完整用量；所有存在用量的尝试也都有完整价格，因此 Token 和总费用可信。
- `partial`：至少一次尝试缺失用量、缺失价格、被取消、读取不完整或解析失败。可以输出已经确认的 Token，但不输出请求总费用。
- `unavailable`：没有任何一次尝试得到完整用量；Token 和费用字段全部省略。

这个状态刻意区分“零”和“未知”。零 Token 只有在 Provider 明确报告零时才成立。

## 6. Provider 采集规则

### 6.1 OpenAI 普通响应

OpenAI 响应保持原样转发。响应体包装器在转发过程中进行有界观察，并在读到 EOF 后解析顶层 `usage.prompt_tokens` 和 `usage.completion_tokens`。观察缓冲超过固定上限时停止采集但继续转发，结算标记为不完整，不能改写或截断客户端响应。

### 6.2 OpenAI SSE

Limen 对发送给 OpenAI 的流式请求加入 `stream_options.include_usage=true`，使上游在结束事件中报告 usage。响应仍然原样逐块转发；观察器只拼接当前 SSE 事件，事件大小有固定上限。用量事件不会被吞掉或改写，客户端继续获得合法的 OpenAI SSE。

### 6.3 Anthropic 普通响应

Anthropic Provider 在现有协议转换过程中读取 `usage.input_tokens` 和 `usage.output_tokens`，同时继续在转换后的 OpenAI Chat Completion JSON 中返回兼容的 `usage` 字段。

### 6.4 Anthropic SSE

Anthropic Provider 在转换事件时从 `message_start` 采集输入 Token，从 `message_delta` 采集输出 Token。文本增量、完成原因和最终 `[DONE]` 继续按 OpenAI SSE 格式实时写出，不等待完整流。

所有流式解析均限制单个事件大小。解析 usage 失败只影响结算；如果事件本身无法完成协议转换，则维持现有流错误语义。

## 7. Fallback 与取消

一次客户端请求的每次真实上游调用都进入 attempt 列表，包括最终没有成为客户端响应的主目标调用。

- 主目标成功时，只结算主目标。
- 主目标产生瞬时错误并切换备用目标时，汇总两个目标明确报告的用量。
- Router 在切换前只允许在剩余 Context 内有界读取被放弃的错误响应，以复用连接并发现可能存在的 usage；达到上限后立即关闭。
- 没有报告 usage 的失败尝试不估算 Token。由于真实费用无法确认，请求结算状态为 `partial`。
- 收到 `2xx` 并开始向客户端发送后，即使 SSE 中途失败也禁止 Fallback。
- 客户端断开继续取消上游；如果最终 usage 尚未到达，则结算状态为 `partial` 或 `unavailable`。
- 请求总预算、单次尝试超时和熔断语义保持不变，结算逻辑不能延长这些时间。

## 8. HTTP 输出

聊天接口在写入状态码前声明以下 Trailer：

```text
X-Limen-Settlement-Status
X-Limen-Input-Tokens
X-Limen-Output-Tokens
X-Limen-Total-Tokens
X-Limen-Cost-USD
```

响应体读取结束后再设置具体值。例如：

```text
X-Limen-Settlement-Status: complete
X-Limen-Input-Tokens: 120
X-Limen-Output-Tokens: 45
X-Limen-Total-Tokens: 165
X-Limen-Cost-USD: 0.000120000
```

输出规则如下：

- `X-Limen-Settlement-Status` 在能够开始上游转发时始终给出。
- `complete` 输出 Token 和费用。
- `partial` 只输出已经确认的 Token；总费用省略。
- `unavailable` 省略全部 Token 和费用字段。
- Trailer 不进入 SSE `data:` 内容，不改变 OpenAI JSON 响应结构。
- HTTP/1.1 代理或客户端可能丢弃 Trailer，因此额度和审计不能只依赖客户端看到的值。

## 9. 日志与安全

每次聊天请求结束后写一条请求级结构化日志，在现有字段基础上增加：

```text
settlement_status
input_tokens
output_tokens
total_tokens
cost_usd
```

日志继续保留 `request_id`、逻辑模型、最终 Provider、路由摘要、尝试次数、HTTP 状态和耗时。缺失字段直接省略。每次尝试的 Provider、上游模型、结果分类和已知结算可以保存在内部记录中；默认请求日志不展开 Prompt、响应正文、API Key 或上游原始错误正文。

日志写入失败、Trailer 不受客户端支持或 usage 解析失败，都不能改变已经确定的 HTTP 响应。

## 10. 错误处理

- 计价配置错误属于启动错误，必须包含模型 ID、目标位置和具体原因，但不能包含密钥。
- 上游返回负 Token、非整数 Token 或越界值时丢弃该次用量，并标记不完整。
- 金额乘法、求和或格式化发生溢出时省略总费用、标记 `partial` 并记录安全错误。
- usage 观察缓冲或 SSE 单事件超过上限时停止该次采集，响应仍继续转发。
- 结算发生在响应生命周期末尾，不能把结算错误改写成 OpenAI JSON 错误。
- 已经向客户端发送任何响应数据后，不得为了获得完整 usage 而继续等待已取消的上游。

## 11. 测试设计

配置与金额测试覆盖：

- 正确解析可选价格和九位小数。
- 拒绝缺少一侧价格、负数、指数、非法字符串、精度超限和溢出。
- 定点乘法、舍入、求和、格式化和溢出保护。
- 无 `pricing` 的配置与兼容模式保持可用。

Provider 测试覆盖：

- OpenAI、Anthropic 普通响应的输入与输出 Token。
- OpenAI 流式请求包含 usage 选项，并能从最终事件采集用量。
- Anthropic 从 `message_start` 和 `message_delta` 采集用量。
- usage 缺失、非法、超大事件和解析失败不会破坏响应转发。
- SSE 第一段数据无需等待结束即可到达读取方，证明没有完整缓冲。

Router 与 HTTP 测试覆盖：

- 单目标成功产生 `complete` 结算。
- Fallback 汇总所有明确报告用量的尝试，并按各自目标价格计算。
- 任一尝试缺少 usage 或价格时为 `partial`，总费用留空。
- 客户端取消继续传播，结算不会阻止响应体关闭。
- Trailer 在读取响应 EOF 后可见，且不会进入 SSE 正文。
- 结算错误不改变上游状态、普通响应正文或 SSE 事件。
- 日志不包含 Prompt、响应正文和 Provider Key。
- 现有超时、熔断、Fallback、普通转发、SSE 和竞态测试继续通过。

最终验证继续执行：

```bash
go clean -testcache
make check
make build
make smoke
make bench
git diff --check
```

## 12. 明确不包含

本阶段不实现：

- 按用户或 API Key 的每日额度、月度额度及超额拦截。
- 单请求金额上限或在途请求中断。
- 数据库、文件账本、账单查询 API 或管理后台。
- 多租户身份、权限和配额管理。
- 根据成本改变路由顺序或自动选择最低价目标。
- 在线价格同步、汇率换算、缓存折扣、批处理折扣或利润加价。
- 对缺失 usage 的 Token 估算。

未来实现每日额度时，应在请求开始前读取已经结算的当日累计值；如果已超额则拒绝新请求。获准执行的请求始终在结束后结算，不因为本次请求使累计值越界而截断响应。

## 13. 验收标准

- OpenAI 与 Anthropic 的普通响应和 SSE 都能产出准确的已报告 Token。
- SSE 保持实时转发、有界内存、取消传播和成功后不可 Fallback 的现有语义。
- 多目标请求按每个目标自己的价格结算所有已报告用量。
- 未知用量或价格不会被表示为零，结算状态能诚实描述完整性。
- 完整请求通过 Trailer 和结构化日志暴露 Token 与成本，不改变 OpenAI 兼容响应体。
- 敏感信息测试、竞态测试、构建、冒烟和基准验证全部通过。
