# Limen 离线配置影响分析计划

## 目标

在配置发布进入审批前，提供一个完全离线的 `limen diff` 命令。它复用启动阶段的严格模型配置解析和控制面的结构 diff，让发布流水线能够回答“候选配置会改变什么”，同时不把上游模型、endpoint 或凭据写入输出。

## 范围

- `--base` 和 `--candidate` 分别读取两个本地模型目录 JSON。
- 两个文件都必须通过 `config.ParseModels` 的字段、能力、价格和路由校验。
- 输出基线版本、候选版本、是否变化以及稳定排序的变化路径和类型。
- 目标路径转换为 `catalog.OpaqueTargetID`，不输出配置值。
- 不读取 API Key，不创建 Provider Client，不连接数据库，不访问网络。

## 实现约束

- 复用 `configstore.Diff`，由 `configstore.PublicDiff` 统一完成安全路径转换，避免 HTTP 控制面和 CLI 各自维护脱敏规则。
- 生产方法使用简体中文用途注释；测试只覆盖成功 diff、空变化和敏感值不泄露等关键边界。
- 解析失败或参数错误返回非零退出码，错误消息不得回显密钥、endpoint 原值或完整配置正文。

## 验收

```bash
go test ./cmd/limen ./internal/configstore -run 'Diff|diff' -count=1
make check
```

成功输出应能被 JSON 工具直接读取，并且只包含 `config_diff`、两个版本哈希、变化标记、变化路径和变化类型。
