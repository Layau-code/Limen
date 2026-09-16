# Limen API Key Store 阶段计划

## 目标

把单租户环境变量 Key 升级为可选的 PostgreSQL 多租户鉴权边界，同时保持静态模式默认行为和现有 OpenAI 兼容 API 不变。

## 实现

- `auth.Authenticator` 统一静态和数据库鉴权器接口。
- PostgreSQL Key Store 按公开前缀查询 Key，只保存 HMAC-SHA-256 摘要、租户、Scope、状态和过期时间。
- 校验通过后只生成 `Principal`，完整 Key 不进入日志、Store、Provider 或 Router。
- `LIMEN_API_KEY_STORE=postgres` 要求 `LIMEN_DATABASE_URL` 和 `LIMEN_API_KEY_HMAC_SECRET`；静态模式继续使用 `LIMEN_API_KEY`。
- 新增 003 迁移，保留旧 Key 管理 API 的建设空间而不把管理能力误混入数据面。

## 明确不包含

- Key 创建、一次性展示、轮换、撤销和管理后台。
- Secret Manager、KMS 和在线 HMAC Secret 轮换。

## 验收

```bash
make check
make build
make smoke
```

测试覆盖格式解析、摘要比较、Scope 和配置模式切换。
