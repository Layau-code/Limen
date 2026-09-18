# Limen 离线决策解释命令

## 目标

把 Limen 的差异化能力变成可直接运行的证据：使用模型目录和 OpenAI Chat 请求快照，在不启动服务、不读取 Provider Key、不访问网络的情况下解释模型选择。

## 约束

- 复用严格 Chat 请求解析和同一 Decision Engine，不复制路由规则。
- 使用固定评估时间，保证相同输入和配置得到相同 `input_hash`/`plan_hash`。
- 输出只包含逻辑模型、Provider、opaque 目标引用、候选原因、策略和哈希。
- 不输出 Prompt、真实 `upstream_model`、密钥或完整内部配置。
- 没有可用目标时命令成功输出候选淘汰原因和 `no_eligible_target`，方便发布前排障。

## 命令

```bash
limen explain --models models.json --request chat-request.json
```

## 验收

- 相同文件连续执行输出字节一致。
- 能看到质量、能力、数据等级和健康约束造成的淘汰原因。
- 输出不包含 Prompt、Provider Key 或真实上游模型名。
- `make check`、`make build` 和命令单元测试通过。
