# Limen 启动就绪边界计划

## 目标

让 readiness 反映真实的接收能力，而不是只反映依赖对象已经组装完成。

## 规则

- 主进程必须先成功绑定 `LIMEN_ADDR` 的 TCP socket。
- 绑定失败时直接返回启动错误，不设置 ready。
- 绑定成功后使用同一个 Listener 调用 `http.Server.Serve`。
- 收到关闭信号后先清除 ready，再执行有限时长的优雅关闭。

## 验收

- 合法地址可以绑定并暴露实际 Listener 地址。
- 非法地址在 Listener 创建阶段返回错误。
- 全量测试、构建、冒烟和 PostgreSQL 集成测试保持通过。
