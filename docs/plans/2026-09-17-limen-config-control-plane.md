# Limen 配置版本控制面实施计划

## 目标

把模型文件的规范内容哈希升级为租户隔离的不可变配置版本，并允许通过鉴权 API 发布新版本。Router 在锁内切换模型目录、路由参数和配置版本，Run 与 Decision Journal 记录实际使用的版本。

## 已完成

- `config.ParseModels` 复用启动文件和数据库配置的同一套严格校验与 SHA-256 版本规则。
- `internal/configstore` 提供内存 Store；`internal/store` 增加 PostgreSQL 配置版本表、RLS、状态约束和唯一已发布索引。
- `GET /v1/limen/configs`、`POST /v1/limen/configs`、`POST /v1/limen/configs/{version}/publish` 使用 `configs:read/write` Scope。
- Router 支持目录和策略原子替换，保留仍存在目标的熔断状态；DecisionInput/ExecutionPlan/Run 记录 `config_version`。
- 配置摘要不返回真实上游模型名，发布版本在 PostgreSQL 重启后恢复。

## 验证

使用 `go test ./... -race` 覆盖版本幂等、租户隔离、发布切换、迁移约束和并发 Router 读写。

## 后续

审批流、旧算法迁移和完整管理 API 不属于本阶段；当前已提供只返回路径与变化类型的配置 diff，以及当前算法版本注册表。
