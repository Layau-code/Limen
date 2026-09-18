# Limen HTTP 依赖装配整理

## 目标

减少 HTTP Handler 构造函数的位置参数，避免新增控制面依赖时出现参数错位，同时保持现有测试和兼容入口不变。

## 方案

- 新增 `httpapi.HandlerOptions`，按字段注入鉴权、Router、Store、Provider 凭据和控制面服务。
- 新增 `NewWithOptions` 作为唯一实际装配入口。
- 保留已有长名称构造函数，将它们收敛为兼容包装，不再增加新的位置参数。
- 主程序使用字段名装配，依赖边界保持可读且可审计。

## 验收

```bash
make check
make build
make smoke
```
