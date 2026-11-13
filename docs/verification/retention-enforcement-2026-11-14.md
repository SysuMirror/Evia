# 长保留期强制回收容量与多租户隔离验证记录（2026-11-14）

## 范围

保留期功能本身已完整交付（策略配置 30..3650 天、`retention-worker`、
归档先删、回补、审计表与控制台，见 feature-coverage 数据生命周期行），
既有证据停留在两层：领域层单测（dry-run/enforce/失败隔离/归档阻断）与
`RetentionExecutor` 的小样本作用域集成测试。feature-coverage 明确剩余项
为「长保留期与多租户隔离**容量**独立验收」。本切片补齐真实 ClickHouse
上的端到端容量证据，不改动任何生产代码：

- 真实 `retention.SweepWorker` 驱动真实 `RetentionExecutor`，跨 3 项目 ×
  2 环境共 6 个策略作用域 + 1 个无策略对照作用域，完成 dry-run、强制
  删除、幂等重扫三个阶段；
- 量化同步 `ALTER DELETE` mutation 的**物理回收量与瞬态放大**（旧/新
  part 共存窗口的真实磁盘 headroom）；
- 证明窗口内近期数据、其他项目/环境与无策略作用域在强制回收后逐行
  精确保留。

策略存储使用进程内 `memSweepStore` 测试替身：PostgreSQL 适配器已有独立
集成套件，且包依赖方向（storage/postgres 测试导入 pipeline）决定
pipeline 包不能反向导入 postgres；cmd 接线另有真实进程冒烟证据。

## 交付内容

- `backend/internal/pipeline/retention_capacity_integration_test.go`
  （integration 标签，`EVIA_TEST_RETENTION_DRILL=1` 门控）。
- `tooling/ha/retention_enforcement_drill.py` 与 21 项纯逻辑单测
  （HA 工具门控累计 223 项），注册进 `ha_suite.py`（套件十→十一，
  dev-stack 层，超时 300s）。

## 负载与作用域设计

| 项 | 值 |
| --- | --- |
| 策略作用域 | 6（3 项目 × development/production），窗口均 30 天 |
| 无策略对照 | 1 个项目，800 行过期数据，预期完全不受 sweep 影响 |
| 每作用域过期行（now-400d） | 1500 |
| 每作用域近期行（now-5d） | 400 |
| 总行数 | 12200（9000 过期 + 2400 近期 + 800 无策略） |
| 写入方式 | 1000 行/批 POST body JSONEachRow，按作用域轮转使单个 part 混合租户与年龄带 |

时间戳相对运行时钟生成，避免固定日期随日历失效；cutoff 由生产
`PlanSweep` 按真实时钟推导（now-30d），测试不伪造时钟。

## 三阶段不变量

1. **dry-run**：6 作用域候选合计严格等于 9000；事实表行数 12200 一行
   不变；审计记录 6 条 `dry_run=true`、全部 succeeded；observer 仅见
   dry-run 标签。
2. **enforce**：删除 9000 行；每作用域 `CountExpired` 归零、近期 400 行
   逐作用域精确保留；无策略对照 800 行原样保留；表剩余严格等于 3200；
   `system.mutations` 6 条全部 `is_done=1`（每作用域一条同步 mutation）；
   审计 6 条 `dry_run=false`，observer 标签闭集正确。
3. **幂等重扫**：6 作用域候选全部为 0，`system.mutations` 数量不增加，
   表字节与强制后完全一致；审计再增 6 条 enforced 记录（累计 12）。

## 物理回收与放大（真实 512 MiB tmpfs dev ClickHouse 26.8）

并发轮询器以 50ms 间隔采样 `system.parts` 全量字节（含非活跃 part），
捕获 `mutations_sync=1` 阻塞期间旧/新 part 共存峰值，两轮独立演练数字
逐值一致：

| 指标 | 两轮一致值 |
| --- | --- |
| 强制前活跃 part 字节 | 250,823 |
| 共存峰值字节 | 1,623,401（首轮直跑）/ 峰值放大 1,343,545（演练器两轮） |
| 峰值/基线比 | **6.36 倍** |
| 强制后稳定字节 | 102,634 |
| 实际回收字节 | 148,188（过期行占行数 73.8%） |
| 重扫后字节 | 102,634（零漂移） |
| mutation 数 | 6/6 完成，重扫新增 0 |
| part_log MutatePart | 24（6 mutation 各重写 4 个 part，软证据） |

**生产含义**：同步 mutation 不是原地删除，而是重写所有命中 part，旧 part
在新 part 落盘后才移除；本数据集单轮 sweep 的瞬态 headroom 需求约为表
当前体积的 6.4 倍。小磁盘（如本机 512 MiB tmpfs）上安排保留期窗口时，
必须按「待清理作用域体积 × 放大系数」预留空间，且建议错峰逐作用域执行
（worker 当前正是每策略独立 mutation、单作用域失败不阻断其余作用域）。

磁盘全局 `free_space` 仅作观测不参与门禁：系统表并发写入使全局空闲噪声
大于本测试的百 KiB 级回收量；表级 `system.parts` 字节才是权威回收信号。

## 先红后绿

1. 千行 JSONEachRow 经 URL query 发送触发 `HTTP 400 URI too long`，
   新增 `postFactRows` 以 POST body 流式写入（与生产 sink 及
   `insertReadinessRows` 同模式）。
2. 首轮候选数 15000 超出 fixture 总量，诊断发现合成项目 UUID
   `11111111-...` 与测试常量 `testProject` 完全撞值导致作用域重复，
   更换为不冲突 UUID 后修复。
3. 演练器门禁把 `system.part_log` 的 MutatePart 数硬断言为每作用域 1：
   该表缓冲刷新（约 7.5s）导致子进程时序下计数不稳定（6/2/0 漂移），
   且一次 mutation 实际重写多个 part（FLUSH LOGS 后实测 24）。修正为
   Go 侧查询前 `SYSTEM FLUSH LOGS`、评估器将 part_log 降级为仅校验
   结构的软证据，权威 mutation 门控只认持久的 `system.mutations`。

## 真实演练结果

```
EVIA_TEST_CLICKHOUSE_USERNAME=evia EVIA_TEST_CLICKHOUSE_PASSWORD=*** \
python3 tooling/ha/retention_enforcement_drill.py
```

两轮独立运行均 `status=passed`，verdict 逐值一致：删除 9000、保留
3200、mutation 6/6、重扫零 mutation、回收 148,188 字节、峰值/基线
6.36、隔离完整；耗时 7.7s/7.0s。演练结束 `evia_pipeline_*` 库与
`metadata_dropped` 均为 0（共享助手 SYNC 删除，见
[Zipf 记录](zipf-distribution-2026-11-13.md)的 tmpfs 修复），日志与
脚本零凭据落盘。

## 边界与诚实声明

- 数据经测试助手直接写入 CH，未经 Kafka/ingest-worker 与公开 API；
  保留期谓词、mutation 与审计路径是生产代码，接入链路正确性由其他
  集成测试覆盖。
- 12200 行、单节点、512 MiB tmpfs 是功能与机理证据：6.36 倍放大系数
  描述 part 重写机理，**不外推生产 TB 级表的绝对 headroom**；生产
  容量验收需在独立基准盘（`compose.benchmark.yaml`）用大作用域重测。
- 归档先删路径本切片未启用（`ScopeArchiver` 为 nil），其 fail-closed
  语义由 `sweep_archive_test.go` 与归档集成测试独立证明。
- 多租户**隔离正确性**已在强制回收下逐行证明；多租户**规模容量**
  （大量并发项目、配额相互影响）仍属独立专项。
