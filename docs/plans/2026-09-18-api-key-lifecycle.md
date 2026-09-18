# Limen API Key 生命周期控制

## 目标

将 PostgreSQL API Key 从“只能校验”扩展为可审计、可撤销的租户凭据控制面，同时保持完整 Key 不落库、不进入日志或 Principal。

## 实现

- 新增 `auth.APIKeyManager` 和 PostgreSQL 实现，支持创建、列表和撤销。
- 创建必须带 `Idempotency-Key`；重复请求返回相同元数据但不再次返回明文。
- Key 使用 `lmn_live_<public_prefix>_<random_secret>` 格式，数据库只保存 HMAC-SHA-256 摘要。
- API Key 控制面只允许 `admin` Scope；事件写入安全审计摘要。
- 新增 011 迁移：Key 操作幂等表、RLS 策略和受控认证查询函数。
- 认证通过安全函数按公开前缀读取最小字段，管理事务设置租户上下文并执行 RLS。

## 边界

本阶段不提供审批流和 Secret Manager；静态 Key 模式仍用于单机开发。创建响应丢失后，系统不会重新显示明文，必须重新创建新 Key。

## 验证

- HTTP 覆盖 admin 鉴权、一次性明文、幂等重试、列表脱敏和撤销。
- PostgreSQL 覆盖摘要存储、认证函数、RLS、跨租户前缀猜测和撤销立即失效。
