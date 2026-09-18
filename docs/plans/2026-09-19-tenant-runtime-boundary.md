# Limen 租户运行边界

## 背景

PostgreSQL API Key Store 可以保存多个租户的 Key，但进程启动时只加载一个 `LIMEN_TENANT_ID` 的配置、Provider endpoint 和本地 Router。若 HTTP 层直接接受其他租户 Principal，请求可能使用错误的模型目录、熔断状态或出站运行时。

## 方案

- 鉴权成功后，HTTP Handler 比较 Principal 的 `tenant_id` 与进程绑定的 `LIMEN_TENANT_ID`。
- 不一致时返回稳定的 `tenant_not_served`，不读取请求正文、不访问 Router 或 Provider，也不暴露租户是否存在。
- 一致的租户才会继续进入 Run、配置、凭据和出站调用路径；Provider 仍按该租户解析加密凭据。
- 多租户部署使用每租户独立进程，共享 PostgreSQL 通过组合键和 RLS 保持数据隔离。

## 验收

- 跨租户 Principal 访问 `/v1/models` 和 Chat 均被拒绝。
- 同租户请求继续把非敏感 `tenant_id` 传入 Provider。
- 不改变静态 Key、Run、配置发布、取消和凭据轮换的既有行为。

