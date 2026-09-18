# Limen 可靠性契约计划

## 目标

把已经存在的 Fallback、预算、取消和流式边界整理成一个离线、确定性的验收入口，让贡献者和面试审阅者可以用一条命令验证 Limen 的核心可靠性，而不需要访问真实 Provider。

## 范围

- 瞬时 HTTP 状态和传输错误允许按计划切换目标。
- 认证、配额和请求转换错误不触发 Fallback。
- 一次请求共享总预算，客户端取消不会继续调用后续目标。
- SSE 开始后不重放请求，响应体和上游 Context 能正确释放。
- Provider 错误分类和 OpenAI 错误边界保持稳定。

## 实施

1. 复用现有 Gateway、Provider 和 HTTP 测试，不新增生产路由逻辑。
2. 新增 `make reliability`，在竞态检测下运行最小故障注入集合。
3. 在 CI、README、AGENTS.md 和设计文档中固定入口和不变量。

## 验收

```bash
make reliability
```

命令必须离线运行并通过 `-race`；失败时优先修复对应契约测试或实现，不通过扩大重试范围掩盖错误。
