# 百万查询失败矩阵复现记录（2026-11-20）

## 目的

在当前 `master`（含身份图四切片）上复现 [2026-11-09 容量报告](query-capacity-2026-11-09.md) 的失败矩阵，为 [查询容量规划](../plans/query-capacity-plan.md) 的根因假设与修复链路提供本机可重复基线。本记录是失败复现与对照，不构成修复交付。

## 环境

- 独立 benchmark Compose 项目（`evia-benchmark`），与开发栈完全隔离；ClickHouse 容器 4 GiB / 服务上限 3 GiB（`max_server_memory_usage=3221225472`）、PostgreSQL 512 MiB、Kafka 保留 2 GiB，与旧证据同构。
- 服务：`serve` / `ingest-worker` / `query-worker` 各 `GOMAXPROCS=2`（`EVIA_QUERY_WORKERS` 默认 2）；接收预算显式容量档 `EVIA_COLLECTION_RATE=20000`、`EVIA_COLLECTION_BURST=40000`。
- 数据：`benchmark.py prepare --uid-cardinality 100000` 新建项目（v3，run 73e8e568），`evia-bench --events 1000000 --batch-size 50 --concurrency 12 --replay-percent 10` 干净完成：1,000,000 唯一 + 100,000 重放全部接受，67.4 秒（≈15.3k 唯一事件/秒）。可见性 drain 核对精确通过（1,000,000 事件 / 100,000 UID）。
- 过程备注：第一轮（v2，并发 16）300 条撞 6 次 `SOURCE_CONFIGURATION_UNAVAILABLE`（源配置缓存瞬时）判不完整，按手册换新配置重灌；首次 drain 在灌库后合并窗口内单次撞 `QUERY_COST_EXCEEDED` 即中止——等合并沉淀后同一查询 0.57 秒返回 1,000,000，属瞬时内存压力，非查询本身超限。

## 结果：`728` 样本，成功 `526`，失败 `202`（全部 `QUERY_COST_EXCEEDED`）

| 工作负载 | 并发 1 | 并发 4 | 并发 8 |
| --- | --- | --- | --- |
| count | 31/31 | 20/30 | 20/30 |
| exact_uid（精确 UV） | 31/31 | 30/30 | 30/30 |
| hour_bucket（小时分桶） | 31/31 | 30/30 | 30/30 |
| section_group（低基数属性分组） | **0/31** | **0/30** | **0/30** |
| high_cardinality_uid_filter | 31/31 | 30/30 | 30/30 |
| property_filter（数值属性筛选） | **0/31** | **0/30** | **0/30** |
| journey（单 UID 行为链） | 31/31 | 30/30 | 30/30 |
| ordered_funnel（两步漏斗） | 31/31 | 30/30 | 30/30 |

## 与 2026-11-09 基线对照

| 指标 | 2026-11-09 | 本次复现 | 结论 |
| --- | --- | --- | --- |
| 矩阵通过 | 514/728 | 526/728 | 同级，通过数略高 |
| 属性类失败 | 182（并发 1 即全挂） | **182**（并发 1 即全挂） | **签名逐量吻合**，确认规划中的 A 类根因：属性查询读取整文档属性载荷（`property_tokens`），在 256 MiB 读 / 512 MiB 内存预算下不可行 |
| 并发资源拒绝 | 32（count C4/C8） | 20（count C4/C8） | 同类，本机合并期内存余量略宽故略少；exact_uid/hour_bucket 并发档本次全过 |
| 其余六类 | 通过 | 通过 | 一致 |

## 结论与下一步

- 规划备忘录的 A/B 两类失败在本机独立环境完整复现，根因假设从「文档推断」升级为「实测确认」。
- 修复主线不变：专用热键列（dedicated layout）生产化闭环——首个生产目录批准 → 槽位迁移提交 → backfill 演练 → 读取切换 → 同条件 728 矩阵复测（验收：属性类 182 → 0，其余不回退）。B 类并发拒绝需先补 `system.query_log`+`system.metrics` 内存记账，再决定统一内存准入设计。
- 原始工件：`/tmp/million-ingestion-v3.json`、`/tmp/million-queries.json`、`/tmp/resources-million.jsonl`（本机，不进仓库）。

## 边界（不声称）

- 合成 Zipf/模运算属性与 UID 分布，不代表真实长尾；30 样本分位数接近最大值，不作生产尾延迟声明。
- 本机单节点容器，不构成生产 HA 或多租户容量结论；并发档失败数受当次合并状态影响。
