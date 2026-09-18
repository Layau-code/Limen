# Limen 文件密钥来源计划

## 目标

在不引入第三方 Secret Manager 或改变现有环境变量行为的前提下，支持容器编排系统通过只读文件挂载密钥。

## 规则

- `LIMEN_API_KEY_FILE`、`OPENAI_API_KEY_FILE`、`ANTHROPIC_API_KEY_FILE` 分别对应三个密钥。
- 文件只在启动时读取一次，并去除首尾空白；空文件或读取失败直接启动失败。
- 对应明文变量与 `_FILE` 同时设置时直接拒绝，避免来源歧义。
- `LIMEN_API_KEY_FILE` 只适用于静态鉴权；PostgreSQL Key Store 的首个 Key 仍由初始化流程提供。
- 密钥内容不能进入日志、配置版本、Decision Journal、Trace 或 HTTP 响应。

## 验收

```bash
go test ./internal/config -run 'TestLoad.*Secret' -count=1
make check
```

文件来源只增加部署输入方式，不改变 Provider、Router、Run 或结算边界。
