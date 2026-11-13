# 上报与查询压测

压测分开记录接收回执、实际可查询事实和查询结果。HTTP 202 只说明请求进入接收流程，必须逐条检查回执；Kafka 确认也不等于 ClickHouse 已经可见。工具仅面向显式准备的本地合成环境，不连接生产业务网站。

## 独立环境

需要项目规定版本的 Go、Python、Docker Compose 2.24.4 或更高版本。使用 `deploy/compose.benchmark.yaml` 覆盖开发配置，独立 Compose 项目、端口和持久卷。Kafka 每主题保留上限 2 GiB、24 小时，ClickHouse 使用磁盘卷，避免开发环境 512 MiB 临时目录限制。百万条测试开始前至少预留 8 GiB 可用磁盘，按实际分片合并和旧分片暂存继续扩大；不能只按最终压缩数据量估算。配置保留 2 GiB 磁盘余量，采样期间接近阈值时中止本轮并保留部分报告。采样器只记录数据，不会自动停止其他进程。

```sh
python3 tooling/benchmark/runtime.py init /tmp/evia-benchmark-deps.env /tmp/evia-benchmark-runtime.env
docker --host=unix:///var/run/docker.sock compose \
  --project-name evia-benchmark --env-file /tmp/evia-benchmark-deps.env \
  -f deploy/compose.dev.yaml -f deploy/compose.benchmark.yaml \
  --profile pipeline up -d --wait
python3 tooling/benchmark/runtime.py prepare /tmp/evia-benchmark-deps.env /tmp/evia-benchmark-runtime.env
go -C backend build -trimpath -o /tmp/evia-benchmark-server ./cmd/evia
go -C backend build -trimpath -o /tmp/evia-bench ./cmd/evia-bench
python3 tooling/benchmark/runtime.py run /tmp/evia-benchmark-runtime.env -- /tmp/evia-benchmark-server migrate
python3 tooling/benchmark/runtime.py run /tmp/evia-benchmark-runtime.env -- /tmp/evia-benchmark-server bootstrap
```

不要提交 `/tmp` 下的配置文件，不要在命令参数中展开密码。显式使用本地 Docker socket；如果设置了 `DOCKER_CONTEXT` 等覆盖配置，先在当前命令环境中移除这些覆盖项。初始化不会覆盖已有配置。`prepare` 仅创建指定命名空间的主题和迁移，不直接写入事件事实或就绪标记。

修改已有服务的内联配置后，显式使用 Compose 的 `--force-recreate` 重建对应容器，保留命名卷。核对 ClickHouse `system.disks.keep_free_space` 为 `2147483648`，不要仅凭 Compose 显示 Running 就认为新配置已经生效。

在三个终端分别运行以下命令，保持进程存活：

```sh
GOMAXPROCS=2 python3 tooling/benchmark/runtime.py run /tmp/evia-benchmark-runtime.env -- /tmp/evia-benchmark-server serve
GOMAXPROCS=2 python3 tooling/benchmark/runtime.py run /tmp/evia-benchmark-runtime.env -- /tmp/evia-benchmark-server ingest-worker
GOMAXPROCS=2 python3 tooling/benchmark/runtime.py run /tmp/evia-benchmark-runtime.env -- /tmp/evia-benchmark-server query-worker
```

默认接收预算为每秒 1,000 条、突发 2,000 条。先验证默认预算的拒绝行为，再测试引擎吞吐。容量轮次在私有 runtime 文件中显式设置 `EVIA_COLLECTION_RATE=20000`、`EVIA_COLLECTION_BURST=40000`，重启这一套测试 API；这不是关闭限流，也不改变生产默认值。记录实际配置和二进制 SHA-256。

## 数据与执行

每个规模、分布、源类别都使用新项目与新配置。相同事件 ID 的属性和发生时间必须保持不变，不能拿同一配置依次生成十万、百万条，否则前缀事件 ID 相同而时间分布改变。`prepare` 经公开管理 API 创建、校验、发布事件，等待真实管道探针，然后激活版本和创建接入来源。

```sh
export EVIA_BENCH_CREDENTIALS=/tmp/evia-benchmark-runtime.env
python3 tooling/benchmark/benchmark.py prepare \
  --api-url http://127.0.0.1:8081 --uid-cardinality 100000 \
  --config /tmp/evia-bench-million.json
python3 tooling/benchmark/observe.py --out /tmp/resources-million.jsonl --duration 1800 --interval 5
```

采样命令在单独终端运行。可重复传 `--pid` 采集指定进程的 RSS 和累计 CPU 时钟计数，不读取命令行或环境。Docker 无 cgroup 隔离时不能把容器统计解释为独立配额或独立进程消耗；报告保留可用性说明。主机统计也包含同机其他进程。

```sh
GOMAXPROCS=2 /tmp/evia-bench --config /tmp/evia-bench-million.json \
  --events 1000000 --batch-size 50 --concurrency 16 --replay-percent 10 \
  --out /tmp/million-ingestion.json
python3 tooling/benchmark/benchmark.py queries \
  --config /tmp/evia-bench-million.json --events 1000000 \
  --samples 30 --concurrency 1,4,8 --drain-timeout 900 \
  --ingestion-report /tmp/million-ingestion.json --report /tmp/million-queries.json
```

同样流程先使用十万条。准备后立即开始上报，固定事件时间覆盖准备时刻之前 30 分钟。接收端只允许一小时回看，生成器最长运行 24 分钟；过期、未接受、发送结果未知、主动丢弃都使报告失败，不自动重试掩盖损失。`--rate` 大于零时为有目标速率、无可用并发槽则明确丢弃的模式，默认模式是有界并发闭环，不能称为恒定到达率压测。

`--replay-percent 10` 在主轮结束后逐字重放前 10% 事件，复用原批次和事件 ID。接收吞吐区分唯一事件和重放事件；唯一事件每秒数的分母包含重放阶段。延迟采用误差上限约 2% 的对数直方图，同时保存样本数和最大值。

查询前核对精确事件数与原始 UID 去重数。八类查询覆盖计数、精确 UID、小时分桶、低基数分组、UID 筛选、数值属性筛选、单 UID 行为链、有序两步访问漏斗。后两类只测试指定身份范围。合成属性与 UID 的模运算相关，不代表真实长尾、随机属性或全站复杂漏斗。

定向复验可使用 `queries --workloads count,exact_uid,hour_bucket`，仅接受已有工作负载名，空项、未知名和重复项均在建立 API 会话前拒绝。默认仍执行全部八类；报告中的 `selected_workloads` 明确保留所选范围。无论选择哪些查询，精确计数/UID 可见性核对和取消检查都会执行。部分矩阵不能标成八类查询全部通过。

记录 `EVIA_QUERY_WORKERS` 的实际值及所有共享 ClickHouse 的工作进程数量。512 MiB 单查询上限不是内存预留，服务端总内存与合并等后台工作仍可能使并发查询被拒绝。如果改变工作任务数，应使用独立报告明确标注配置，保留原配置失败结果，不将两者作为仅改变 SQL 的对照。

查询报告保存首次观测和重复查询两类结果。首次观测不表示冷 OS/数据库缓存，端到端时延包含提交、轮询、取结果。延迟分位数只统计成功样本，失败按错误码单独计数；失败较多时不能仅凭成功样本的分位数评价稳定性。每并发每查询至少 30 个重复样本，p99 在这种样本量下接近最大值，不能当成稳定生产尾延迟。查询限额、终态错误和部分失败必须保留，禁止仅统计 HTTP 提交成功率。

## 验证与归档

```sh
go -C backend test -race ./cmd/evia-bench ./internal/ingestion
python3 -m unittest discover -s tooling/benchmark -p 'test_*.py'
```

原始安全报告、命令配置、硬件/依赖版本、后台任务数量、开始结束时间、二进制哈希和失败分析一起归档。通过 ClickHouse `system.query_log` 的 `evia-analysis-` 查询记录补充 `read_rows`、`read_bytes`、`memory_usage` 和 `query_duration_ms`，将排队/轮询时间与引擎时间区分。业务事实只能从公开接收接口产生。

规模成功不自动证明千万用户并发。持续混合读写、长保留期、高基数、多租户、故障恢复、多副本和真实磁盘介质仍需独立测量。当前配置只是一套可复现的单机基线。

## 摄取积压恢复与故障注入

只读查询矩阵（见 [query-capacity 验证](../verification/query-capacity-2026-11-09.md)）不覆盖写入侧在依赖故障期的行为。积压恢复演练在真实 Kafka→ingest-worker→ClickHouse 管线上注入一次有界 sink 故障，量化「积压形成→冻结→恢复排空」：

- 故障期 worker 对持有的批次无限指数退避重试、停止 Poll 且不提交偏移，消费组滞后随生产增长并冻结；故障期零事件落库。
- 恢复后同一 worker 排空积压、滞后归零，去重视图计数严格等于生产数（精确一次）。
- 滞后经生产 `KafkaReader.SampleLag` 采样，与 `LagMonitor` 上报走同一代码路径。

运行（凭据经环境变量传入，默认指向 dev 测试栈）：

```sh
EVIA_TEST_CLICKHOUSE_USERNAME=evia EVIA_TEST_CLICKHOUSE_PASSWORD=*** \
python3 tooling/ha/backlog_recovery_drill.py
```

纯逻辑门控：

```sh
python3 -m unittest tooling.ha.test_backlog_recovery_drill
```

实测两轮（单 broker、单分区、200 事件含 180 积压）：故障窗口约 9.3 秒内滞后冻结于 180、零新增落库，恢复后排空约 320ms（约 560 事件/秒）、最终滞后为 0、无重复无丢失、无主题/数据库残留，详见[验证记录](../verification/backlog-recovery-2026-11-13.md)。该速率是当前批大小与退避配置下的单机基线，不能外推多分区水平扩展；稳态混合读写与长尾分布容量仍需独立专项。

## 稳态混合读写

只读矩阵测量冻结数据，积压演练测量无读流量下的写故障，混合读写演练补上「边写边读」的稳态证据：一个生产 ingest worker 持续写 ClickHouse 的同时，4 个读协程并发跑有界分析（总数、分组、UID 切片），断言精确一次落地、零读失败、计数单调非降、p95 延迟在执行预算内，并从 `system.query_log` 取峰值内存。

运行：

```sh
EVIA_TEST_CLICKHOUSE_USERNAME=evia EVIA_TEST_CLICKHOUSE_PASSWORD=*** \
python3 tooling/ha/mixed_workload_drill.py
```

纯逻辑门控：

```sh
python3 -m unittest tooling.ha.test_mixed_workload_drill
```

实测两轮（单 broker、单 CH、600 事件约 5 秒写入、4 并发读）：读样本 974/993 个零失败、p50 10ms、p95 66/67ms、峰值查询内存约 14.3 MiB、视图计数严格等于生产数、四个读协程计数全部单调非降，详见[验证记录](../verification/mixed-read-write-2026-11-13.md)。该结果是存储层小规模基线，读绕过公开 API（鉴权/排队/异步轮询），端到端延迟仍以 514/728 矩阵为准；高基数长尾分布与多副本拓扑容量仍需独立专项。

## 高基数长尾身份分布

混合读写演练使用接近均匀的身份分布，长尾演练补上真实倾斜：4000 个事件按确定性规则分布到 800 个身份——20 个热点身份吸收 80% 事件（各 160 个），780 个稀疏身份轮转其余 20%（最稀疏仅 1 个）。Go 装载器与 Python 评估器共享同一零基分配规则，逐身份计数与 `uniqExact` 基数都做精确断言。

运行：

```sh
EVIA_TEST_CLICKHOUSE_USERNAME=evia EVIA_TEST_CLICKHOUSE_PASSWORD=*** \
python3 tooling/ha/long_tail_drill.py
```

纯逻辑门控：

```sh
python3 -m unittest tooling.ha.test_long_tail_drill
```

实测两轮：800 身份全覆盖、观测热点份额 0.8、抽样热点/稀疏身份计数与分布期望逐一精确、`uniqExact(uid)=800`、热点/深尾/基数三类查询 p95 均 31-34ms，详见[验证记录](../verification/long-tail-distribution-2026-11-13.md)。该倾斜是确定性两段均匀轮转（头尾比 160），复现数量级差异但不等同真实 Zipf；单机小数据结果不外推多节点，真实分布压测需先从生产事件采样。

## Zipf 幂律身份分布

两段均匀的长尾合成不是幂律；Zipf 演练按 P(rank) ∝ 1/rank^1.0 生成身份，配额经 Hamilton 最大余数法取整为严格守恒的整数席位（Go 装载器与 Python 评估器同构，实测双对数斜率跨语言六位小数一致），再验证秩-频双对数斜率、逐身份精确计数与 Top-K 排序。

运行：

```sh
EVIA_TEST_CLICKHOUSE_USERNAME=evia EVIA_TEST_CLICKHOUSE_PASSWORD=*** \
python3 tooling/ha/zipf_distribution_drill.py
```

纯逻辑门控：

```sh
python3 -m unittest tooling.ha.test_zipf_distribution_drill
```

实测两轮：500 身份全覆盖、观测斜率 -1.0295（理论 -1.0，容差 0.15）、最热/最冷 589/1、抽样计数与 Top-5 顺序精确、三类查询 p95 26-30ms，详见[验证记录](../verification/zipf-distribution-2026-11-13.md)。该演练已注册进 `tooling/ha/ha_suite.py`（套件现为十一个演练）。

指数支持生产拟合入口：`--exponent s`（同步注入 Go 侧 `EVIA_TEST_ZIPF_EXPONENT`），闭集 `(0, 3]`，默认 1.0，非法值在任何网络检查前 fail-closed；评估器按摘要中的指数自动对齐理论斜率，容差不变。固定 4000 事件/500 身份负载下，Hamilton 全覆盖成立域为 s ≤ 1.1805（二分数值证明），更陡指数会在深尾产生零席位并由 Go 断言 fail-closed，需先扩大事件总量。dev 栈 512 MiB tmpfs 上连续演练曾因异步 `DROP DATABASE` 延迟删除与系统日志膨胀耗尽磁盘（NOT_ENOUGH_SPACE），共享测试助手已改为 `DROP DATABASE ... SYNC`；连续演练前仍应核对 `system.disks` 空闲空间与 `metadata_dropped` 为空。生产验收应先从真实事件采样估计 s，再以同一演练重放，当前实测指数仍为人为选取。

## 长保留期强制回收容量

保留策略与执行器的功能正确性已有单元与小样本集成测试，本演练补上强制回收的物理容量与多租户隔离证据：真实 `SweepWorker` 驱动真实 `RetentionExecutor`，对 3 项目 × 2 环境共 6 个策略作用域（各 1500 行过期 + 400 行近期）加 1 个无策略对照（800 行过期）共 12200 行数据，依次执行 dry-run、enforce 与幂等重扫。

运行：

```sh
EVIA_TEST_CLICKHOUSE_USERNAME=evia EVIA_TEST_CLICKHOUSE_PASSWORD=*** \
python3 tooling/ha/retention_enforcement_drill.py
```

纯逻辑门控：

```sh
python3 -m unittest tooling.ha.test_retention_enforcement_drill
```

实测两轮（数字逐值一致）：dry-run 候选 9000 且零修改；强制删除 9000、近期 2400 行与无策略 800 行逐行保留、`system.mutations` 6/6 同步完成；重扫零新 mutation、表字节零漂移；物理回收 148,188 字节，50ms 并发轮询实测旧/新 part 共存峰值达基线 **6.36 倍**——同步 mutation 重写全部命中 part，小磁盘排期必须按作用域体积预留 headroom 并错峰执行。`system.part_log` 受缓冲刷新影响仅作软证据，权威门控为 `system.mutations` 与表级字节；磁盘全局空闲因系统表噪声不参与断言，详见[验证记录](../verification/retention-enforcement-2026-11-14.md)。该演练已注册进 `tooling/ha/ha_suite.py`（套件现为十一个演练）。12200 行单节点结果不外推 TB 级生产表，大规模 headroom 需在独立基准盘重测。
