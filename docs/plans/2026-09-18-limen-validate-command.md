# Limen 离线配置预检计划

## 目标

把严格模型目录校验前移到发布流水线，避免必须启动网关、配置 Provider Key 或访问网络才能发现配置错误。

## 实施

- 增加 `limen validate --models <path>` 子命令。
- 复用 `config.ParseModels` 和现有目录转换，不复制模型字段校验逻辑。
- 输出规范配置生成的 `config_version`、模型数量、目标数量和 Provider 数量。
- 预检过程不读取环境密钥、不创建 Provider Client、不访问网络，也不输出真实上游模型名。

## 验收

- 无 Provider Key 时仍能成功校验有效模型目录。
- 无参数、文件不存在、JSON 无效或字段不支持时返回非零退出码。
- 成功输出不包含 `upstream_model`、密钥或请求正文。
- `make check`、`make compatibility`、`make reliability`、`make integration`、`make build`、`make smoke` 和 `git diff --check` 通过。
