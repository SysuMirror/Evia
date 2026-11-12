# 优雅排空与就绪门控验证记录（2026-11-12）

## 范围

面向多副本滚动部署的连接排空能力：`observe.DrainGate` 就绪门控、`EVIA_SHUTDOWN_DELAY` 配置、`runHTTP` 的「先摘流量再停监听」顺序。修复的真实缺口是——旧实现在 SIGTERM 后 `/readyz` 仍返回 204，负载均衡器会持续向正在退出的实例转发新请求，客户端表现为连接重置。

## 单元与集成测试证据

- `internal/platform/observe/drain_test.go`（5 项）：门控初始 ready、单向不可逆转换、nil 安全零值、排空时立即 503 且带 `Retry-After: 5`、排空期间不再调用依赖探针、探针失败映射 503。
- `cmd/evia/runtime_test.go` 新增 2 项：
  - `TestHTTPDrainFailsReadinessBeforeClosingListener`：300ms 排空窗口内，SIGTERM 后 `/readyz` 在 200ms 内变 503，而一个在信号前发起的 `/slow` 在途请求释放后仍完整返回 204，进程在有界时间内退出。
  - `TestHTTPDrainZeroDelayKeepsImmediateShutdown`：`delay=0` 保持立即停机语义且门控仍翻转。
- `internal/platform/config` 新增 `TestShutdownDelayBounds`：默认 5s、接受 `0s` 与上限 `30s`、拒绝 `31s`/负值/非法格式，且非 serve 进程不继承该 HTTP 专属设置。
- `go vet` 与受影响包全部通过：`cmd/evia`、`internal/platform/observe`、`internal/platform/config`。

## 真实进程冒烟（serve on :18090，EVIA_SHUTDOWN_DELAY=3s）

以 fullstack 环境启动真实 serve，健康基线 `healthz=204 readyz=204`，随后发送 SIGTERM 并按 0.5s 间隔轮询：

```
t=0.016s  readyz=503 healthz=204
t=0.536s  readyz=503 healthz=204
t=1.054s  readyz=503 healthz=204
t=1.574s  readyz=503 healthz=204
t=2.091s  readyz=503 healthz=204
t=2.610s  readyz=503 healthz=204
total shutdown time: 3.114s; process exited
```

关键事实：

- SIGTERM 后 **16 毫秒**就绪探针即失败，负载均衡器可立刻摘除该副本；整个 3 秒排空窗口存活探针始终 204，不会被存活检查误杀。
- 进程在 3.11 秒退出（3s 传播延迟 + 约 0.1s 监听关闭），受控于既有 15s 监督上限。
- 同一 shell 内 `wait` 捕获真实退出码为 **0**，日志零 ERROR，监听端口确认释放。

## 边界与诚实声明

- 本次验证覆盖单进程的探针与排空时序；多副本间实际摘除速度取决于负载均衡器健康检查间隔，runbook 要求 `EVIA_SHUTDOWN_DELAY` 大于该间隔，真实 LB 传播需在部署环境复核。
- 超过 10 秒 HTTP 停机预算的长轮询/流式请求仍会被监督截止时间终止，这是有意的有界保护而非缺陷。
- PostgreSQL/Redis/ClickHouse 的有状态故障转移属于托管服务职责，不在 Evia 进程排空范围内。
