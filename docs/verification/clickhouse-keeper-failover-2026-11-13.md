# ClickHouse Keeper 复制故障切换验证记录（2026-11-13）

## 范围

为有状态存储 HA 的最后一块拼图补齐 ClickHouse 复制能力验证：两节点
ReplicatedMergeTree 经三票 ClickHouse Keeper 仲裁协调，前端由纯标准库
TCP 故障转移代理提供单一稳定端点，生产 pipeline sink 与 analysis 读取
代码零改造。在真实容器拓扑上注入节点故障，验证写入失败可观测、同进程
经代理恢复、Keeper 驱动副本自动重归且两节点数据严格收敛。

生产单节点 `MergeTree` 迁移未做任何修改；复制 schema 是独立评审的演练
工件，与迁移 001 中「复制部署需独立拓扑与仲裁策略」的既有约束一致。

## 交付内容

- `deploy/compose.ha-clickhouse.yaml`：两个 clickhouse-server（内嵌
  Keeper，id 1/2）+ 一个独立 clickhouse-keeper（id 3），三票仲裁容忍
  单节点故障；静态 IP 桥接网络、命名卷、loopback 端口发布。
- `deploy/clickhouse-ha/001_replicated_event_facts.sql`：逐列镜像迁移
  001/003/006/008 全部插入面字段（30 列）的 ReplicatedMergeTree 表与
  009 版 29 槽读取视图；004 的 MATERIALIZED 辅助列与 005 的投影不进入
  插入载荷，按约定省略。
- `tooling/ha/ch_tcp_proxy.py`：无依赖 asyncio TCP 代理，
  BackendSelector 纯逻辑健康状态机（11 项单测）。
- `tooling/ha/clickhouse_failover_drill.py`：演练编排与纯逻辑评估
  （13 项单测），全部纳入 `make check` 的 ha-tools-test 门禁。
- `internal/pipeline/replicated_failover_integration_test.go`
  （integration 构建标签）：真实 sink 经代理写入的基线与受害者测试。

## 环境

- ClickHouse 26.8.36.3（digest 锁定），Docker 20.10.24，cgroup v1。
- 拓扑：ch-1（172.31.79.10）、ch-2（.11）各持一副本并内嵌 Keeper，
  keeper-3（.12）为独立第三票；raft 内部端口 9235 与 Keeper 客户端
  端口 9234 分离；代理 ch-lb（.20）监听 8123，宿主映射 18131。
- 固定演练口令、无 TLS，数据与日志为命名卷，演练结束 `down -v` 零残留。

## 真实演练结果

命令（工具链激活后）：

```
python3 tooling/ha/clickhouse_failover_drill.py
```

两轮独立运行均 `status=passed`：

| 指标 | 第一轮 | 第二轮 |
| --- | --- | --- |
| Keeper 仲裁 | 三票建立，两节点各 1 条连接 | 同左 |
| 集群成员 | 2 副本 | 2 副本 |
| 成功插入（生产 sink 经代理） | 300 | 300 |
| 节点停止期失败尝试 | 1 | 1 |
| 存活节点计数 | 300 | 300 |
| 旧节点重归后双节点计数 | 300 / 300 | 300 / 300 |
| `system.replicas` 重归后 | is_readonly=0 且 queue_size=0 | 同左 |
| 恢复后 Keeper 连接 | 两节点均 1 | 两节点均 1 |

证明的四个不变量：

1. **故障期写入失败而非静默降级**：节点停止后经代理的插入立即报错，
   代理被动健康检查在秒级内摘除故障后端。
2. **同进程自动恢复**：代理路由到存活节点后，原 sink 无需重启即恢复
   插入，受害者恢复探针成功。
3. **Keeper 协调零丢失收敛**：失败尝试保持同一事件身份重试，最终两
   副本计数均严格等于成功数，无缺口、无重复。
4. **副本自动重归**：旧节点重启后经 Keeper 重新注册并清空复制队列，
   无需任何人工数据修复。

## 演练过程中暴露并修复的真实问题

1. Redis 8 同类的 Compose `$` 插值问题未出现，但 raft 端口误与 Keeper
   客户端端口同号触发 `RAFT_ERROR` → 拆分 9234/9235；独立 Keeper 缺
   `<logger>` 与 `data_storage_path` 启动即退 → 补齐。
2. 镜像默认用户配置与自定义 evia 用户的网络访问需要独立 users 配置
   显式声明，经挂载 users.d 解决。
3. sink 载荷含 003/006/008 新增列而初版复制表只镜像 001 → HTTP 400，
   补齐为完整 30 列写入面 schema 与 009 版视图。
4. EventID 必须满足 UUID 闭包、count 查询必须显式别名、DDL 切分需先
   剥离注释行，均由真实测试先后暴露并修复。
5. `clickhouse-client -tAc` 中 `-c` 被解析为 `--config-file` → 统一
   改用 `-q`。
6. **cgroup v1 下 pids_limit 256 在复制追赶期被线程数打满（252/256），
   容器无法再创建任务（docker exec 报 read init-p）** → PID 预算提升
   到 1024；期间曾尝试收紧 `max_thread_pool_size`/background 池，反而
   导致 AsyncLoader 启动死锁，最终回退全部线程池设置，仅保留 PID 预算
   与查询 profile 级边界。
7. 重启瞬间 `docker exec` 连接重置 → 演练轮询改为容错重试。

## 门禁

- `make check` 退出码 0：HA 工具目录累计 50 项纯逻辑单测（含本切片
  24 项）、后端全套 `-race` 与前端构建。
- 真实容器演练两轮通过，结束后容器、网络、命名卷零残留。

## 边界与诚实声明

- 本验证是单宿主功能级复制证据，使用无 TLS 固定口令与异步复制，不构成
  跨可用区容灾或 RPO=0 结论；生产必须经正式迁移评审上线 Replicated 表、
  跨故障域部署 Keeper 并启用 TLS 与受控 LB。
- 未覆盖 Keeper 自身多数派失联（同时宕两票）、脑裂、分布式 DDL 仲裁与
  分片（多 shard）扩容场景。
- Redis Cluster、同步复制仲裁策略与生产容量矩阵仍未验证。
