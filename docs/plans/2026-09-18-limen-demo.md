# Limen 离线演示计划

## 目标

提供一个不依赖数据库、Provider 密钥或外部网络的确定性演示，展示能力契约选模、Fallback 执行和配置草稿影响分析。

## 场景

1. 客户端请求 `model=auto`，声明 `data_class=internal` 和最低质量等级 `3`。
2. Decision Engine 选中满足契约的 `smart-model`，排除质量或数据等级不满足的目标。
3. 第一个 OpenAI 目标返回瞬时 `503`，Router 按计划切换到 Anthropic 目标并成功返回。
4. 将历史决策快照重放到只保留 Anthropic 的草稿目录。
5. 模拟一次 Run 准入、决策绑定和已知成本结算。
6. 输出路由路径、Attempt 数、选中的逻辑模型、草稿 Provider、影响分析和 Run 结算摘要。

## 安全边界

- 演示 Provider 只存在于进程内，不创建 HTTP Server。
- 不读取环境变量中的 API Key，不访问网络，不输出 Prompt、Response 或上游模型名。
- 输出字段固定，便于脚本和文档重复验证。

## 验收

```bash
make demo
```

输出必须稳定包含 `openai:503>anthropic:200`、`attempts=2`、`selection.requested_model=auto`、`selection.selected_model=smart-model`、`selection.data_class=internal`、`selection.rejected[0]=basic-model:quality_tier_too_low`、`draft_provider=anthropic`、`impact_detected=true`、`provider_calls=2`、`run.state=active`、`run.settlement_status=complete`、`run.in_flight=0`、`run.settled_cost_nano_usd=250000` 和 `run.decision_bound=true`。
