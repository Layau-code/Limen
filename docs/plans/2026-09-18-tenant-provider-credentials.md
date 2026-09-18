# Limen 租户级 Provider 凭据实施计划

## 目标

让多租户鉴权边界继续贯穿到真实 Provider 出站调用，避免不同租户共享进程启动时的 Provider 密钥。

## 约束

- `ChatRequest` 只携带非敏感 `tenant_id`，不携带明文密钥。
- 凭据解析必须同时绑定 `tenant_id`、Provider 名称和 endpoint ID。
- 启用加密凭据存储后，凭据缺失或解析失败直接拒绝出站，不回退到其他租户或进程共享密钥。
- 未启用凭据存储时保留环境变量密钥的单机兼容模式。
- Provider 不读取 HTTP 请求或 Principal，只消费已经标准化的租户标识和 Context。

## 实现

1. 在 Provider 请求中加入租户上下文和按请求凭据解析函数。
2. OpenAI、Anthropic 在构造上游请求前解析对应租户的密钥。
3. HTTP Handler 将鉴权 Principal 的 `tenant_id` 传入 Router；Router、Decision Journal 和日志不持久化密钥。
4. 主程序装配加密凭据解析器，并要求当前配置租户为实际使用的 Provider 配置凭据。
5. 覆盖跨租户解析、缺失凭据、并发轮换、取消传播和两种 Provider 的回归测试。

## 验收

```bash
make check
make integration
make build
make smoke
```

通过后，凭据隔离从数据库存储边界扩展到每次真实上游请求；静态环境变量模式仍只用于单机开发。
