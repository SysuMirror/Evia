# 生产就绪证据索引（Production Readiness Evidence Index）

更新日期：2026-11-14 UTC。本文件把分散在 `docs/verification/` 的 33 份验证
记录按「推动到正式生产规模使用」的硬阻塞主题归并，明确每个主题：已由
哪些真实依赖证据闭环、还差什么、剩余项为何不能在单机环境完成。它是
目标推进的导航，不替代各验证记录的原始数据；结论以被引用记录为准。

判定口径：

- **已闭环（本机可验证）**：在真实容器/进程上有两轮可复现证据，且对应
  纯逻辑或集成测试进入 `make check` 门禁。
- **待外部环境**：本质依赖多机房拓扑、真实厂商服务或生产流量分布，
  单宿主容器无法产生有效证据，强行模拟会给出误导性结论。
- **待运营决策**：需要业务侧输入（如真实事件采样、热键选键），不是
  工程探针能独立决定的。

## 1. 高可用与故障转移 —— 已闭环（单宿主拓扑内）

| 子主题 | 关键证据 | 门禁资产 |
| --- | --- | --- |
| 进程级优雅排空 | [graceful-drain](verification/graceful-drain-2026-11-12.md)：SIGTERM 后 readyz 立即 503、healthz 保持 204、在途请求完整 | DrainGate 单测 + 真实进程冒烟 |
| Redis 哨兵/集群 | [redis-sentinel](verification/redis-sentinel-failover-2026-11-13.md)、[redis-cluster](verification/redis-cluster-failover-2026-11-13.md)：fail-closed、零双花、槽位路由 | HA 套件 + 纯逻辑单测 |
| PostgreSQL 流复制/同步仲裁 | [postgres-streaming](verification/postgres-streaming-failover-2026-11-13.md)、[postgres-quorum](verification/postgres-sync-quorum-2026-11-13.md)：脑裂防护、pg_rewind 收敛 | HA 套件 |
| ClickHouse Keeper | [ch-failover](verification/clickhouse-keeper-failover-2026-11-13.md)、[keeper-quorum](verification/clickhouse-keeper-quorum-2026-11-13.md)：多数派失联只读、块去重幂等 | HA 套件 |

**待外部环境**：跨故障域布票、TLS 认证的真实多机房拓扑分发——单宿主
无法验证网络分区与证书链，需要独立故障域环境。

## 2. 摄取流水线韧性 —— 已闭环

| 子主题 | 关键证据 |
| --- | --- |
| 积压恢复 | [backlog-recovery](verification/backlog-recovery-2026-11-13.md)：故障期不提交偏移、恢复后去重视图计数精确 |
| 混合读写 | [mixed-read-write](verification/mixed-read-write-2026-11-13.md)：600 事件精确一次、并发读零失败 |
| 身份分布 | [long-tail](verification/long-tail-distribution-2026-11-13.md)、[zipf](verification/zipf-distribution-2026-11-13.md)：倾斜/幂律分布计数精确，Zipf 开放生产拟合入口 |
| 准备度套件 | [ha-readiness-suite](verification/ha-readiness-suite-2026-11-13.md)：11 个演练统一注册表、声明式前置、0/1/2 退出码 |

**待运营决策**：Zipf 指数的真实生产拟合值（需真实事件采样，固定负载
全覆盖边界 s≤1.1805 已由二分证明）。

## 3. 保留期与数据生命周期 —— 已闭环（容量模型完整）

| 子主题 | 关键证据 | 工程结论 |
| --- | --- | --- |
| 强制回收正确性 | [retention-enforcement](verification/retention-enforcement-2026-11-14.md) | 6 作用域 mutation 6/6、作用域隔离、重扫幂等 |
| headroom 规模模型 | [retention-headroom-scale](verification/retention-headroom-scale-2026-11-14.md) | 5.39–7.94x、规模稳定，按 9 倍预留 |
| 边界月份混合分区 | [retention-boundary-partition](verification/retention-boundary-partition-2026-11-14.md) | 混合分区不额外抬高 headroom（假设被否定） |
| 多 part 形态 | [retention-multipart-headroom](verification/retention-multipart-headroom-2026-11-14.md) | 6–7 part 仅高约 3–4%，9 倍余量仍充分 |

**待外部/扩展验证**：几十上百 part 极端形态与 TB 级表的 headroom 复测
（探针已具备，需更大基准环境）；STOP MERGES 会冻结 mutation 这一引擎
事实已记录，禁止用该方式构造多 part。

## 4. 查询容量、护栏与多租户隔离 —— 已闭环

| 子主题 | 关键证据 |
| --- | --- |
| 基线容量与 514/728 | [query-capacity](verification/query-capacity-2026-11-09.md)：默认双工作任务矩阵，失败记录保留未粉饰 |
| 物理读门控 | [property-layout-reads](verification/property-layout-reads-2026-11-10.md)、[dedicated-columns](verification/property-layout-dedicated-columns-2026-11-12.md)、G3 百万行矩阵 |
| 多租户内存隔离 | [tenant-query-isolation](verification/tenant-query-isolation-2026-11-14.md)：单查询 512MiB 封顶、嘈杂邻居 containment、聚合上界演绎保证 |
| 护栏完整性与取消 | [query-guard-rails](verification/query-guard-rails-2026-11-14.md)：164/159/396/307/158/241/394 全码表，KILL 作用域隔离 |

**待外部/扩展验证**：数十上百租户的连接池化与排队大规模容量；514/728
中属性分组/筛选的 182 个失败在专用列热键选定前保持开放。

**待运营决策**：专用列热键选键、有界 backfill 与 514/728 端到端复测。

## 5. 备份、恢复与异地容灾 —— 已闭环（厂商行为除外）

| 子主题 | 关键证据 |
| --- | --- |
| PG/CH 备份恢复演练 | [backup-restore-drill](verification/backup-restore-drill-2026-11-12.md)：三轴校验、篡改负例 |
| 自动周期调度 | [backup-schedule](verification/backup-schedule-2026-11-12.md)：flock、保留期、textfile 指标、告警防漂移 |
| 异地对象存储副本 | [object-storage-replication](verification/object-storage-replication-2026-11-12.md)：SigV4 与 botocore 字节级交叉验证 |
| 备份完整性 | CH 原生 BACKUP/RESTORE + sha256 manifest，篡改/多余/缺失/外版本/空产物负例单测全覆盖 |

**待外部环境**：parquet 格式、S3 多段上传、SSE-KMS 服务端加密与跨区域
厂商特有行为——需真实厂商凭证与网络，SigV4 客户端本体已交叉验证。

## 6. 可观测性 —— 已闭环

五个进程角色指标、SLO 录制规则（12）、分级告警（17，含备份演练两条）、
Grafana 11 仪表盘、Alertmanager 路由，均经 `check_monitoring.py` 静态
防漂移门禁与 promtool 单测；证据见 [metrics](verification/metrics-2026-11-09.md)
与 SLO runbook。

## 剩余硬阻塞汇总（按解锁条件分类）

| 阻塞项 | 解锁条件 | 当前可做的准备 |
| --- | --- | --- |
| 生产拓扑分发（跨故障域/TLS） | 多机房/独立故障域环境 | 单宿主 HA 证据已就绪，拓扑 runbook 已写 |
| TB 级与极端 part headroom | 更大基准盘/专用压测环境 | headroom 探针可直接扩规模复用 |
| 多租户连接池化大规模容量 | 高连接数基准环境 | 单查询护栏与聚合上界已证 |
| 热键选键 + 514/728 复测 | 运营数据决策 | 专用列代码侧已完结，门控与探针齐备 |
| 真实 Zipf 拟合指数 | 生产事件采样 | 参数化入口与闭集校验已交付 |
| parquet/多段上传/SSE-KMS | 真实厂商环境 | SigV4 客户端与 fail-closed 编排已验证 |

## 结论

所有**不依赖外部环境与运营决策**的生产化硬阻塞，均已有真实依赖两轮
证据并接入门禁，单机层面无遗留的可独立验证项。剩余六项的共同特征是
需要多机房拓扑、真实厂商服务、更大基准资源或业务数据输入；在这些
条件具备前，继续在已饱和主题上叠加探针不会降低生产风险，应转向等待
外部条件或处理明确的产品功能（留存/分群/归因/会话回放，见
feature-coverage「未实现」行）。
