# 摄取流水线积压恢复与故障注入验证记录（2026-11-13）

## 范围

补齐 `feature-coverage.md` 容量与恢复工程中明确未验证的「持续混合读写、
积压恢复与故障注入」缺口。此前 514/728 只读查询矩阵测量了读侧并发，
HA 系列演练测量了依赖故障的切换语义，但**写入侧在依赖故障期间的 Kafka
积压形成与恢复后排空曲线**始终没有端到端证据。本切片在真实
Kafka→ingest-worker→ClickHouse 管线上注入一次有界 sink 故障，验证：

1. 故障期 worker 持批重试、不提交偏移，消费组滞后增长到完整积压并冻结；
2. 故障期零事件落库（不静默推进偏移、不丢数据）；
3. 恢复后同一 worker 排空积压、滞后归零，每个生产事件精确一次落地
   （去重视图计数等于生产数，raw 表不少于生产数）；
4. 给出可复现的排空速率与有界恢复时间，作为容量矩阵的一个实测单元。

本切片不改动任何生产 Go 代码；新增物为一个集成测试、一个演练脚本与
纯逻辑单测、文档。

## 交付内容

- `backend/internal/pipeline/backlog_recovery_integration_test.go`：
  `TestBacklogRecoveryUnderSinkOutage`（integration 标签，
  `EVIA_TEST_BACKLOG_DRILL=1` 门控）。`gateSink` 以原子开关包装真实
  ClickHouse sink，关闭时直接返回 `ErrSinkUnavailable` 且不触碰内层
  sink——这正是生产 sink 在 ClickHouse 宕机时向上暴露的错误。滞后经
  生产 `KafkaReader.SampleLag`（同组内存提交位点 + broker log-end）采样，
  kgo 客户端并发安全，worker 阻塞重试期间测试协程采样不会抢占分区。
- `tooling/ha/backlog_recovery_drill.py` 与 18 项纯逻辑单测（纳入
  `make ha-tools-test`）：排空速率、滞后序列冻结判定、报告解析与
  精确一次/有界排空不变量评估；编排层先做 Kafka TCP 与 ClickHouse HTTP
  连通性检查，再驱动 Go 测试并校验其 JSON 报告。

## 关键机制（先红后绿确认）

- **持批背压**：`Worker.process` 对 sink 写入走无限指数退避 `retry`，
  故障期同一批次反复重试、不进入偏移提交阶段，并停止 Poll 以限制内存。
  因此 broker log-end 随生产持续前进而提交位点冻结，`SampleLag` 观察到
  的滞后单调增长到完整积压。
- **滞后探针选型**：初版用独立客户端经 `OffsetFetch` 读 worker 组提交
  位点，实测始终拿到 -1（新客户端不持有该组内存位点）。改为直接复用
  生产 `KafkaReader.SampleLag`，既稳定又顺带验证了真实监控代码路径，
  单测与演练均以此为准。
- **无歧义写入**：与 HA 演练的异步超时歧义不同，gate 关闭时根本不调用
  内层 sink，故障期零部分写入，因此恢复后 raw 行数与去重视图严格相等，
  是干净的精确一次场景。

## 环境

- 开发测试栈：Apache Kafka 4.4.0（单 broker，`127.0.0.1:19092`）、
  ClickHouse 26.8.36.3（`127.0.0.1:18123`），均为当前运行中的 dev 容器。
- 每次运行使用唯一后缀的 topic / 消费组 / ClickHouse 库，测试结束由
  `t.Cleanup` 删除主题与库；实测两轮后无残留主题或数据库。
- 负载：基线 20 事件健康排空 → 关 gate 后分块生产 180 事件（共 200），
  worker `BatchSize=10`、退避 20ms→200ms。

## 真实演练结果

命令（工具链激活，凭据经环境变量传入）：

```
python3 tooling/ha/backlog_recovery_drill.py
```

两轮独立运行均 `status=passed`：

| 指标 | 第一轮 | 第二轮 |
| --- | --- | --- |
| 生产事件总数 | 200（基线 20 + 积压 180） | 同左 |
| 故障窗口 | 9280ms | 9278ms |
| 故障期滞后采样 | 16 次全部冻结于 180 | 同左 |
| 峰值滞后 | 180（=完整积压） | 180 |
| 故障期落库行数 | 20（仅基线，零新增） | 同左 |
| sink 重试尝试 | 69 | 69 |
| 恢复后排空耗时 | 320ms | 323ms |
| 排空速率 | 562.5 事件/秒 | 557.3 事件/秒 |
| 最终滞后 | 0 | 0 |
| 去重视图计数 | 200 | 200 |
| raw 表计数 | 200（无重复无丢失） | 200 |
| 残留主题/数据库 | 无 | 无 |

第一轮 Go 测试原始摘要：

```
{"backlog":180,"baseline":20,"drain_ms":323,"drain_rate_per_s":557.276,
 "final_lag":0,"lag_samples":[180 x16],"outage_ms":9279,"peak_lag":180,
 "produced":200,"raw_count":200,"sink_attempts":69,
 "type":"backlog_recovery_summary","view_count":200}
```

## 边界与诚实声明

- 故障以进程内 gate 注入而非真正停掉 ClickHouse 容器，避免干扰共享 dev
  栈；它复现的是 worker 实际看到的 `ErrSinkUnavailable` 与重试/偏移语义，
  不覆盖 ClickHouse 进程崩溃、网络分区或磁盘满等基础设施级故障（那些由
  HA 系列的容器停断/网络分区演练覆盖）。
- 单 broker、单分区、单 worker：排空速率反映当前批大小与退避配置下的
  单机能力，不能外推多分区水平扩展后的吞吐，也不构成千万用户容量声明。
- 180 事件的小积压在数百毫秒内排空；生产量级的积压恢复时间须按实测
  排空速率线性估算并以更长时间的压测复核，本证据只证明机制正确与有界。
- 读侧并发容量仍以 514/728 矩阵为准，本切片不改变其失败记录；混合读写
  的稳态容量矩阵（读写同时加压、长尾身份/字段分布）仍待后续专项。
