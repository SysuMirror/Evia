# ClickHouse Keeper 多数派失联验证记录（2026-11-13）

## 范围

在单节点停止的 Keeper 复制故障切换（见
`clickhouse-keeper-failover-2026-11-13.md`）之外，补齐
`feature-coverage.md` 列出的「Keeper 多数派失联」硬阻塞项。复用同一套
三票 Keeper 拓扑（ch-1/ch-2 内嵌 Keeper + 独立 keeper-3，单分片双副本
ReplicatedMergeTree），在真实容器上验证：

1. 少数派丢失（1/3 票下线）可容忍：剩余两票维持 leader，写入继续；
2. 多数派丢失（2/3 票下线）fail-closed：无 leader、副本翻只读、插入
   有界超时失败且绝不假成功，而 SELECT 持续返回最后持久化数据，整个
   故障窗口可见行数冻结；
3. 生产 sink 同构表现：有界 HTTP 客户端在仲裁缺失期返回错误、同一
   sink 在仲裁恢复后续写成功，ReplicatedMergeTree 块去重保证重试幂等；
4. 分阶段恢复：仅恢复一票即重建多数派并恢复写入（此时第二个数据副本
   仍停止），副本归队后 Keeper 自动重放、队列清空、双副本逐值收敛。

本切片不改动任何生产 Go 代码；新增物为测试代码、演练脚本与文档。

## 交付内容

- `tooling/ha/clickhouse_keeper_quorum_drill.py` 与 20 项纯逻辑单测
  （纳入 `make ha-tools-test`）：多数派算术、有界插入 SQL 渲染、
  159 超时分类、冻结读数判定、victim 事件流评估等闭集。
- `backend/internal/pipeline/keeper_quorum_integration_test.go`：
  `TestKeeperQuorumSinkVictim` 走生产 `NewClickHouse` sink，直连存活节点
  ch-1，HTTP 客户端超时收紧到 8 秒，记录逐次插入事件并在结束时核对
  存活副本计数。
- 复用 `deploy/compose.ha-clickhouse.yaml`，以独立 project
  `evia-ha-ch-quorum` 运行，命名卷随 `down -v` 清理。

## 演练中确认的关键语义

1. **选举窗口竞态**：被停的恰好是 leader 时，`system.keeper_cluster`
   的 `is_leader` 标志会先于副本会话恢复完成出现，选举后首个写入仍可能
   超时。演练因此以「有界重试直到写入成功」证明恢复的有界性，而非假设
   切换瞬间可写；第一轮实测 seq 10 超时（5141ms）后 seq 11 在 229ms
   成功。
2. **多数派丢失不快速失败，而是阻塞到超时**：无 leader 时
   ReplicatedMergeTree 插入挂在 WaitForAsyncInsert，默认
   `wait_for_async_insert_timeout=120000ms` 后返回
   `Code 159 TIMEOUT_EXCEEDED`。演练以
   `SETTINGS wait_for_async_insert_timeout=5` 把探针收敛到约 5.15 秒
   有界失败；生产 sink 的 30 秒（演练收紧为 8 秒）HTTP 超时先于服务端
   120 秒到期，错误统一收敛为 `ErrSinkUnavailable`，由 Kafka 租约/DLQ
   重试承接。
3. **只读与读存活**：多数派丢失后 `system.replicas` 立即翻
   `is_readonly=1, is_session_expired=1`；SELECT 不依赖 Keeper 仲裁，
   全程返回故障前数据。
4. **异步插入超时是歧义结果**：客户端收到 159 不保证块丢失。第一轮三个
   故障探针 seq 100/101/102 在窗口内均不可见、读数冻结于 6，但仲裁
   恢复后三个块全部经完整复制落地，双副本最终序列完全一致。演练不断言
   超时行不存在，只断言「窗口内无确认、无新可见行」，并在报告中如实
   列出歧义序列。生产侧的安全网是 ReplicatedMergeTree 默认块去重
   （同事件同块哈希）与 argMin 视图的身份去重，victim 同序列重试因此
   不产生重复行。
5. **分阶段恢复**：仅启动 keeper-3（ch-2 仍停止）即凑齐 2/3 票，
   leader 重建、副本解除只读，seq 200 在 167ms 写入成功；ch-2 归队后
   两副本 `is_readonly=0, queue_size=0` 并逐值收敛。

## 环境

- ClickHouse 26.8.36.3（digest 锁定），Docker Compose v2，独立 bridge
  `172.31.79.0/24`；无 TLS、固定演练口令，`down -v` 清理命名卷。
- Keeper：3 票 Raft（id 1/2 内嵌，id 3 独立），
  `operation_timeout_ms=10000`、`session_timeout_ms=30000`。
- 健康判据：`system.keeper_cluster WHERE is_leader=1` 计数（健康=1，
  多数派丢失=0）；`system.replicas` 的只读/队列字段。

## 真实演练结果

命令（工具链激活后）：

```
python3 tooling/ha/clickhouse_keeper_quorum_drill.py --duration 70
```

两轮独立运行均 `status=passed`、`topology_removed=true`，结束后容器、
网络、命名卷零残留：

| 阶段证据 | 第一轮 | 第二轮 |
| --- | --- | --- |
| 健康仲裁 | 3 票，多数派=2，leader=1 | 同左 |
| 基线复制收敛 | 4 行双副本一致 | 4 行双副本一致 |
| 少数派丢失（停 keeper-3） | seq10 先超时（5141ms），seq11 在 229ms 成功 | seq10 首次尝试 203ms 成功 |
| 故障前动态基线 | 6 行 | 5 行 |
| 多数派丢失副本状态 | `is_readonly=1, is_session_expired=1` | 同左 |
| 故障窗口有界插入探针 | 3 次均 Code 159，约 5.15s | 3 次均 Code 159，5145/5147/5145ms |
| 窗口内读数 | 三次均冻结于 6，SELECT 返回 6 | 三次均冻结于 5，SELECT 返回 5 |
| 恢复一票后写入 | seq 200，167ms 成功（ch-2 仍停止） | seq 200，157ms 成功 |
| Go sink victim | 82 成功 / 5 失败，存活副本=82 | 75 成功 / 6 失败，存活副本=75 |
| 故障窗口（victim 视角） | 65988ms | 65943ms |
| 副本归队 | 双副本 `(readonly=0, queue=0)` | 双副本 `(readonly=0, queue=0)` |
| 最终双副本序列 | `[1,2,3,4,10,11,100,101,102,200]` 逐值一致 | `[1,2,3,4,10,100,101,102,200]` 逐值一致 |
| 歧义探针落地 | 100/101/102 恢复后全部落地 | 100/101/102 恢复后全部落地 |

第一轮故障窗口探针原始错误（三次同构）：

```
Code: 159. DB::Exception: Wait for async insert timeout (5000 ms) exceeded:
While executing WaitForAsyncInsert. (TIMEOUT_EXCEEDED)
```

## 边界与诚实声明

- 单宿主容器拓扑，不验证跨故障域网络、TLS 与认证；明文 loopback 仅为
  演练资产，不是生产模板。
- 歧义块的最终落地依赖服务端异步插入队列在进程存活期间保留块；若进程
  在仲裁恢复前崩溃，块仍可能丢失，应用侧须以 Kafka 位点未确认为准
  重放，而不是把客户端超时当作确定失败。
- 本证据为功能级仲裁验证，不含生产容量矩阵、跨地域 Raft 延迟与
  Keeper 磁盘 I/O 压力下的选举时延分布。
- Evia 不运营 Keeper 仲裁本身；生产需托管 Keeper/ClickHouse Operator
  完成跨故障域布票、受控重启与监控告警，应用侧要求仅限于有界客户端
  超时与幂等重试，二者均已在生产链路具备。
