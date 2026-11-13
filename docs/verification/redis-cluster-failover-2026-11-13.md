# Redis Cluster 分片故障切换验证记录（2026-11-13）

## 范围

在 Redis Sentinel（单主发现）之外补齐分片模式：接入预算与准入统计两个
客户端支持 Redis Cluster 槽位路由，三种连接模式（单 URL / Sentinel /
Cluster）在启动期互斥 fail-closed。在真实 3 主 3 从集群上注入槽位主节点
故障，验证 fail-closed、同进程拓扑刷新恢复、零双花与旧节点自动归队。

默认单端点与 Sentinel 路径行为保持逐字节不变；未配置 Cluster 时不产生
任何新连接或新行为。

## 交付内容

- `budget.ClusterConfig` / `admissionstats.ClusterConfig` 与
  `clusterOptions`，经 `redis.NewClusterClient` 接入；`Limiter.client`
  与 `Recorder.client` 改为 `redis.UniversalClient` 以容纳三种客户端。
- config 层 `EVIA_REDIS_CLUSTER_ADDRS`（1..8 个种子）、
  `EVIA_REDIS_CLUSTER_TLS_ENABLED`（默认 true），明文仅限 loopback 加
  insecure 开关；`runtime.admissionStatsConfig` 防御性复制地址切片。
- `deploy/compose.ha-redis-cluster.yaml`：6 节点 3 主 3 从，独立命名卷
  持久化 `nodes.conf`，幂等 init 作业经 `redis-cli --cluster create`
  分配全部 16384 槽。
- 两个包的 Cluster 单测（闭集、互斥、TLS、安全边界）与 config/runtime
  回归测试；两个 integration 基线测试与预算受害者测试。
- `tooling/ha/redis_cluster_failover_drill.py` 与 13 项纯逻辑单测
  （含 CLUSTER NODES 槽位范围解析），纳入 ha-tools-test 门禁。

## 关键设计：拓扑刷新间隔

go-redis 的 ClusterClient 只在收到 MOVED/ASK 或到达
`ClusterStateReloadInterval`（默认 60s）时刷新槽位表。主节点宕机返回的是
连接错误而非 MOVED，默认间隔会让客户端在整个故障窗口盯着旧主。因连接
固定 RESP2（无推送通知），显式设置 3 秒刷新间隔，把故障 RTO 限定为
「集群选举时间 + 一次拓扑刷新」，单测钉住该值。

## 环境

- Redis 8.10.3 alpine（digest 锁定），Docker 20.10.24，cgroup v1。
- 拓扑：redis-node-1/2/3 为初始主（.10/.11/.12），node-4/5/6 为副本
  （.13/.14/.15），`cluster-node-timeout` 5000ms；宿主经 bridge 路由
  直达内部 IP，种子用 loopback 发布端口 16479-16484。
- 无 TLS、固定演练口令；命名卷随 `down -v` 清理，演练结束零残留。

## 真实演练结果

命令（工具链激活后）：

```
python3 tooling/ha/redis_cluster_failover_drill.py
```

两轮独立运行均 `status=passed`：

| 指标 | 第一轮 | 第二轮 |
| --- | --- | --- |
| 槽位覆盖 | 16384/16384，3 主 | 同左 |
| 预算成功预留 | 343 | 371 |
| 主节点停止期失败尝试 | 11 | 8 |
| 实际消耗 token 单位 | 342,101,960 | （落在精确借记带内） |
| 精确借记带 | [342,100,000, 343,000,000] | 通过 |
| 自动提升的新主 | redis-node-5 | redis-node-5 |
| 旧节点归队 | 3 主恢复、无 fail/handshake | 同左 |

证明的四个不变量：

1. **故障期 fail-closed**：槽位主停止后预留返回
   `COLLECTION_BUDGET_UNAVAILABLE`，无静默接受。
2. **同进程自动恢复**：集群自动把副本提升为主，客户端经 3 秒拓扑刷新
   跟随新主，进程不重启、无人工切换。
3. **零双花**：节点级重试保持关闭，实际消耗始终落在
   `成功数×单事件成本 − 最大补充量` 与 `成功数×单事件成本` 的闭区间内，
   任何一次歧义重放都会让偏差超出一个完整事件成本。
4. **旧主自动归队**：持久化 `nodes.conf` 让回归节点联系存活对等节点并
   转为副本，集群恢复 3 主且全部槽位覆盖。

## 演练过程中暴露并修复的真实问题

1. 误抄 ClickHouse 镜像摘要前缀导致 docker 拒绝拉取 → 改回正确 redis
   digest；宿主端口与开发 Redis 16379 冲突 → 整体后移到 16479-16484。
2. 客户端包校验要求种子为 loopback → 测试用宿主发布端口做种子，学习到
   的内部 IP 由客户端按 bridge 路由直连（与 Sentinel 学习主节点同机制）。
3. **默认 60s 拓扑刷新间隔超过演练窗口，客户端持续拨测已死主节点** →
   显式 `ClusterStateReloadInterval=3s`，这是本切片最核心的生产参数。
4. init 作业非幂等，对已建集群重复 `--cluster create` 退出 1；一次性
   grep 在集群瞬时转换态误判 → 改为状态检查加分步轮询直到 ok。
5. **演练第二次就绪检查硬编码轮询已停止的 node-1 导致假失败** → 就绪
   函数接受服务参数，故障后改轮询存活节点；槽位归属查询同步参数化。
6. **tmpfs 在容器重建时丢失 nodes.conf，旧主以空配置重启无法归队** →
   改为每节点独立命名卷持久化集群状态，归队自动完成。

## 门禁

- `make check` 退出码 0：HA 工具目录累计 63 项纯逻辑单测（含本切片
  13 项）、后端全套 `-race` 与前端构建。
- 真实容器演练两轮通过，结束后容器、网络、命名卷零残留。

## 边界与诚实声明

- 本验证是单宿主功能级分片证据，使用无 TLS 固定口令，不构成跨可用区
  容灾结论；生产必须跨故障域部署主从并启用 TLS 与集群总线鉴权。
- 未覆盖多键跨槽事务（Evia 键已 hash-tag，单桶永远单槽）、槽位手动
  resharding、大规模重定向风暴与网络分区脑裂场景。
- 同步复制仲裁、生产容量矩阵仍未验证。
