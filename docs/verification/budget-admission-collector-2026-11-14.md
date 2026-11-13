# 真实 Redis 预算经 Collector 准入全链路验证记录（2026-11-14）

## 范围

收口采集准入链路最后一段未经真实依赖验证的环节。在此之前，预算拒绝
路径只有两类隔离证据：

- `source_collection_retry_test.go` 用预算替身证明 HTTP 层把
  `fault.Limited` 映射为 429、`fault.Unavailable` 映射为 503；
- `platform/budget` 包的集成测试直连真实 Redis，证明 Lua 限流器本身的
  原子借记、作用域隔离、回补、故障关闭与时钟防护。

两者之间缺少「真实 Redis 限流器注入生产 `Collector`，耗尽如何在入队
之前被拒绝」的端到端证据。本切片补齐该缝隙，且只需要 Redis——因为
`Collector.Collect` 在任何持久队列发布之前先执行 `ReserveCollection`。

## 交付内容

新增 `backend/internal/ingestion/budget_admission_integration_test.go`
（`integration` 构建标签），三个测试：

1. `TestRealBudgetAdmissionThroughCollector`：满 burst 批次（50 事件）
   正常受理且仅入队一次；下一个单事件以 `fault.Limited` 失败、返回
   status 0、队列调用次数不增加、拒绝汇流排记录一次
   `COLLECTION_BUDGET_EXCEEDED`。
2. `TestRealBudgetAdmissionScopeIsolation`：同一项目的 `development`
   作用域耗尽后，独立的 `production` 桶仍完整可受理，证明桶按
   项目/环境二元作用域隔离。
3. `TestRealBudgetAdmissionRefillRecovery`：Rate=1/s、Burst=50，
   耗尽后立即断言仍被拒绝（单个令牌需整整 1 秒累积，race 检测器
   调度下也确定性耗尽），再以 10ms 轮询断言在 3 秒截止时间内随
   令牌回补重新受理，用截止时间而非固定睡眠保证负载下确定性。

辅助件：

- `realAdmissionLimiter`：经 `EVIA_TEST_REDIS_URL` 构造真实限流器，
  键前缀 `evia:test:admission:<uuid>` 与任何其他测试隔离，启动先
  Ping 探活，缺依赖即失败关闭；
- `resetAdmissionBucket`：通过公开 API `ResetBucket` 按作用域清理
  （`Limiter` 不暴露裸客户端，包外无法 SCAN/DEL）；
- `admissionCollector`：复用 `collector_test.go` 的既有 source/plan/
  queue 替身与 `rejectionSinkStub`，仅把预算替身替换为真实限流器，
  不新造重复类型。

无生产代码变更，默认路径行为逐字节不变。

## 环境

- dev Redis 8.10.3 容器 `evia-pipeline-dev-redis-1`（healthy），
  宿主 loopback 端口 16379，RESP2。
- 策略：耗尽类用例 Rate=0.001（闭集允许的最小速率，测试窗口内实质
  无回补）、Burst=50（= 契约批量上限，一批确定性耗尽）；恢复用例
  Rate=1、Burst=50。
- 凭据不写入任何脚本或文件，运行时经
  `docker exec ... printenv REDIS_PASSWORD` 读取并仅存活于当次
  shell 环境。

## 真实运行结果

命令（工具链激活后，经标准 Makefile 目标）：

```
EVIA_TEST_REDIS_URL=redis://:<password>@127.0.0.1:16379/0 \
  make budget-test
```

该目标以 `-race -tags=integration` 同时选择 `platform/budget` 与
`ingestion` 两个包。两轮独立运行均 `ok`：

| 包 | 第一轮 | 第二轮 |
| --- | --- | --- |
| platform/budget（含 4 个拓扑用例 SKIP 带原因） | 1.631s ok | 1.646s ok |
| ingestion（三个准入用例全 PASS） | 8.868s ok | 8.835s ok |

单测粒度的 -race 实测耗时：ThroughCollector 0.18s、ScopeIsolation
0.32s、RefillRecovery 1.16s（与 1 事件/秒回补、约 1 秒后恢复的
设计吻合）。

每轮结束后以 `SCAN 0 MATCH 'evia:test:*' COUNT 1000`
独立交叉核验，两轮均只返回游标 0、零键，ResetBucket 清理无残留。

## 先红后绿：验证过程中修复的两个真实问题

1. **回补断言竞态（本切片新测）**：初版恢复用例取 Rate=200/s，
   约每 5ms 回补一个令牌；满 burst 批次返回到下一次预留之间的
   Redis 往返间隔在 race 检测下即可累积出一个令牌，导致「耗尽后
   立即拒绝」断言偶发失败。改为 Rate=1/s 后，单令牌需 1 秒累积，
   毫秒级调用间隔不可能回补出完整事件成本，耗尽确定性恢复，而
   恢复仍稳定落在 3 秒截止时间内（实测约 1.16 秒）。
2. **`make budget-test` 在单 Redis 环境的既有破损**：HA 工作包
   加入的 `TestClusterBaselineReserve` / `TestSentinelBaselineReserve`
   在缺少拓扑环境变量时以 `t.Fatal` 失败，而 CI 的 budget 作业按
   [dependencies runbook](../runbooks/dependencies.md) 只启动单节点
   Redis，导致该目标在本机与 CI 都无法通过（拓扑用例实际由 HA
   演练脚本带 `-run` 过滤和专用变量调用）。改为缺变量时 `t.Skip`
   并附原因，与同文件 victim 用例及 pipeline 包的可选演练约定
   一致；变量存在但拓扑不可达时 Ping 仍 `t.Fatalf`，fail-closed
   语义不变。

## 证明的不变量

1. **预算拒绝先于入队**：`Collect` 在发布前调用预留，耗尽时
   `fault.Limited` 直接返回，queue 替身调用次数恒为 1，超限事件
   不可能进入 Kafka 侧。
2. **拒绝可观测且归因正确**：拒绝汇流排收到恰好一次
   `COLLECTION_BUDGET_EXCEEDED`，与 HTTP 429 映射测试的原因码闭合。
3. **作用域隔离**：桶键由项目/环境二元作用域派生，一个环境耗尽
   不消耗或冻结兄弟环境的令牌。
4. **回补恢复有界**：窗口速率下令牌按服务器时间回补，耗尽不是
   永久状态，恢复时间受速率约束并在测试截止时间内发生。

## 与既有证据的衔接

- HTTP 状态码映射（429/503）仍由 `source_collection_retry_test.go`
  的替身测试钉住；本记录证明其下方的 `fault.Limited` 能由真实
  Redis 确定性产生，两段证据首尾相接。
- 限流器单机语义（原子借记、无部分借记、时钟回拨防护、策略不一致
  失败关闭）由 `platform/budget` 集成测试覆盖；本记录证明这些语义
  穿过 Collector 装配缝后不变形。
- Redis Sentinel/集群故障切换中的 fail-closed 与零双花由
  [redis-sentinel](redis-sentinel-failover-2026-11-13.md)、
  [redis-cluster](redis-cluster-failover-2026-11-13.md) 覆盖。

## 诚实边界

- 队列是进程内替身，本测试不经过真实 Kafka；持久队列侧的积压与
  精确一次语义由 backlog-recovery 演练独立覆盖。
- 单 Redis 节点，不验证故障切换；高可用路径归 Sentinel/集群演练。
- Redis 整体状态丢失（键被清空）后桶会按当前策略重新初始化、
  重新获得满 burst，这是 Redis 令牌桶设计的固有属性，配置值与
  分布式限流测试不能替代该场景下的硬配额保证，feature-coverage
  保留该项说明。
- 集成测试不进入默认 `make check`（需活容器与凭据），已并入
  Makefile 的 `budget-test` 目标：该目标现选择 `platform/budget`
  与 `ingestion` 两个包，共用同一单节点 Redis 前置条件；集群/
  哨兵拓扑用例在缺少专用变量时显式 SKIP，由 HA 演练在拓扑就绪时
  带变量执行。
