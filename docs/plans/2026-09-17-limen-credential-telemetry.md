# Limen 凭据与基础可观测性实施计划

## 目标

在不把 Provider 凭据带入 Router、日志或决策快照的前提下，提供可轮换的安全存储边界，并加入低基数指标，方便验证生产可靠性。

## 已完成

- `internal/credentialstore` 使用 AES-GCM、随机 Nonce 和附加认证数据，绑定 tenant、Provider、endpoint。
- 内存和 PostgreSQL 凭据存储支持轮换、解析和撤销；Provider `SetAPIKey` 使用读写锁，轮换不影响在途调用。
- `LIMEN_CREDENTIAL_MASTER_KEY` 可选启用启动时读取加密凭据；未找到时回退到对应 Provider 环境变量。
- `internal/telemetry` 只接受固定指标名和有界标签；`/metrics` 需要 `admin` Scope。

## 后续

Provider 凭据创建/轮换管理 API、Secret Manager 接入、OpenTelemetry Trace/Exporter 和多实例 PostgreSQL 故障注入测试仍留在 1.0 生产化阶段。
