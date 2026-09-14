# Limen 第三阶段实施计划

## 目标

将写死的模型前缀路由升级为启动时只读的模型注册表，并新增需要鉴权的 OpenAI 兼容 `GET /v1/models`。

## 实施内容

1. 增加 `LIMEN_MODELS_FILE` JSON 加载、字段校验、重复 ID 检查和按需 Provider Key 校验。
2. 引入 `ModelRegistry`，由 Router 解析逻辑模型并替换真实上游模型。
3. 保留未配置文件时的 OpenAI 与 Anthropic 前缀兼容模式。
4. 增加 `/v1/models`，复用聊天接口鉴权并按模型 ID 稳定排序。
5. 回归验证普通响应、SSE、错误、超时、取消和安全日志。
6. 同步更新 README、协作规范和设计文档。

## 验收

- 配置模式只接受已注册逻辑模型，并只要求实际使用的 Provider Key。
- 兼容模式继续支持 `gpt-*`、`o1-*`、`o3-*` 和 `claude-*`。
- `/v1/models` 在两种模式下均返回正确、稳定的 OpenAI 风格列表。
- `make check` 与 `go build -o /tmp/limen-phase3 ./cmd/limen` 通过。
