# Limen 鉴权边界阶段计划

## 目标

把现有单一 Bearer Key 校验整理为可替换的租户身份边界，为最终设计中的多租户 API Key、Scope 和审计能力提供最小实现。

## 本阶段实现

- 新增 `internal/auth`，定义 `Principal`、固定 Scope 和单机静态鉴权器。
- 使用常量时间比较校验 Bearer Key，鉴权失败不向下游暴露原始 Key。
- HTTP 接口声明所需 Scope：数据面使用 `inference`，Run 控制面区分读写，Dry Run 需要 `decisions:read`。
- 带 `X-Limen-Run-ID` 的 Chat 额外需要 `runs:write`，避免普通推理 Key 修改 Run 状态。
- `LIMEN_API_SCOPES` 在启动时严格解析；`LIMEN_TENANT_ID` 来自静态 Principal，并用于 Run 哈希和 Store 调用。

## 明确不包含

- 数据库 API Key Store、Key 创建/轮换、HMAC 摘要持久化。
- 多租户管理 API、配置管理 API 和跨实例权限缓存。

## 验收

```bash
make check
make build
make smoke
```

测试覆盖错误 Key、未知 Scope、接口 Scope 拒绝、Principal 租户绑定和 Run Chat 的额外写权限。
