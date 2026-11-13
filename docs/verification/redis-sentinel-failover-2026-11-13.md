# Redis Sentinel 故障切换验证记录（2026-11-13）

## 范围

为接入预算（`platform/budget`）与准入拒绝统计（`platform/admissionstats`）
补齐 Redis Sentinel 主节点发现能力，并在真实的 1 主 1 从 3 哨兵容器拓扑上
注入主节点故障，验证 Evia 客户端在不重启进程的前提下自动跟随提升的新主，
且故障窗口内 fail-closed、不存在歧义扣费重放。

默认单端点 `EVIA_REDIS_URL` 路径保持逐字节不变；Sentinel 为独立环境变量
闭集，未配置时不产生任何行为变化。

## 交付内容

- `budget` / `admissionstats` 新增 `SentinelConfig` 与 `failoverOptions`，
  经 `redis.NewFailoverClient` 接入；`MaxRetries=-1`（预算）、`MaxRetries=0`
  （统计）、RESP2、有界超时、连接池闭集与 5 分钟连接寿命全部保留。
- `config.Read` 新增 `EVIA_REDIS_SENTINEL_ADDRS` / `_MASTER` /
  `EVIA_REDIS_PASSWORD` / `EVIA_REDIS_SENTINEL_PASSWORD` /
  `EVIA_REDIS_SENTINEL_TLS_ENABLED`，与 `EVIA_REDIS_URL` 互斥 fail-closed，
  明文仅限 loopback 加显式 insecure 开关。
- `deploy/compose.ha-redis.yaml`：静态 IP 桥接网络上的五节点临时拓扑。
- `tooling/ha/redis_failover_drill.py`：纯标准库演练脚本，评估逻辑拆为
  无依赖纯函数并由 13 项单测覆盖，纳入 `make check`。
- 集成测试 `sentinel_integration_test.go`（两个包，`integration` 构建标签）。

## 环境

- Redis 8.10.3（digest 锁定的 alpine 镜像），Docker 20.10.24，Linux 宿主。
- 拓扑：`redis-master`（172.31.77.10）、`redis-replica`（.11）、三哨兵
  （.21/.22/.23，quorum 2，`down-after` 5s，`failover-timeout` 10s）。
- 宿主经 Docker bridge 路由直达内部静态 IP；数据节点全部 tmpfs，无认证、
  无 TLS，端口仅发布到 loopback，演练结束 `down -v` 零残留。

## 真实演练结果

命令（工具链激活后）：

```
python3 tooling/ha/redis_failover_drill.py
```

两轮独立运行均 `status=passed`，关键指标：

| 指标 | 第一轮 | 第二轮 |
| --- | --- | --- |
| 哨兵检测到提升耗时 | 7466 ms | 约 7 s 量级 |
| 提升后新主 | 172.31.77.11 | 172.31.77.11 |
| 成功预留 | 747 | 748 |
| 失败预留（均为 `COLLECTION_BUDGET_UNAVAILABLE`） | 11 | 11 |
| Redis 侧实际消耗 token 单位 | 746,100,960 | 747,100,960 |
| 精确借记带 | [746,100,000, 747,000,000] | [747,100,000, 748,000,000] |
| 独立 redis-cli 交叉读取 tokens | 253,899,040（与受害者摘要一致） | 252,899,040（一致） |
| 旧主重启后 | 以 slave 身份重新接入且 `master_link_status:up` | 同左 |

证明的三个不变量：

1. **故障窗口 fail-closed**：主节点停止后所有预留以
   `COLLECTION_BUDGET_UNAVAILABLE` 失败，无静默接受。
2. **零双花**：`MaxRetries=-1` 在 Sentinel 路径保留，Redis 侧消耗落在
   「成功次数 × 单事件成本 − 最大 refill」之上的精确借记带内；任何一次
   重放都会造成 1,000,000 单位偏差，远超带宽（两轮最大 refill 仅 900,000）。
3. **同进程自动恢复**：哨兵提升副本后，长生命周期客户端无需重启即恢复
   成功预留；旧主回归后自动成为新主副本。

## 演练过程中暴露并修复的真实问题

1. Redis 8 哨兵启动时强制解析监控主机名，compose 默认网络下 musl 解析器
   拒绝 `redis-master` 名称 → 改用自定义桥接网络与静态 IP。
2. 初版受害者参数（burst 300、rate 0.001）触发策略 24 小时回补闭集拒绝
   → 速率提升到 0.02/s 并收紧 burst 上限，同时保证窗口内 refill 不足一个
   事件成本，不破坏固定库存语义。
3. 恢复探针误把正常的 `COLLECTION_BUDGET_EXCEEDED` 当成切换失败 → 故障
   证据收紧为必须出现 `COLLECTION_BUDGET_UNAVAILABLE`，burst 放大到 1000
   保证窗口内不耗尽。
4. 旧主重启瞬间 `redis-cli` 连接重置被当作致命错误 → 重归轮询改为容错
   重试，直到复制状态恢复。
5. 基线断言未计入两次预留间的毫秒级 refill → 改为 5000 单位容差的紧区
   间断言，仍能区分任何一次完整事件的漏扣或重扣。

## 门禁

- `make check`（含 13 项演练纯逻辑单测、后端全套 `-race` 单测与前端构建）
  退出码 0。
- 两个 Redis 包新增 Sentinel 单测 25 项（配置闭集、互斥、failover 字段
  映射、TLS、默认 URL 路径回归），config 与 cmd 层各新增独立测试文件。

## 边界与诚实声明

- 本验证是单宿主桥接网络上的功能级故障切换证据，不构成生产容量或跨
  可用区容灾结论；生产拓扑必须使用真实故障域、认证与 TLS。
- 未覆盖 Redis Cluster（分片槽位路由）与哨兵节点自身大面积失联的场景，
  两者属于后续独立验收项。
- PostgreSQL 流复制/提升与 ClickHouse Keeper 复制拓扑仍未在本仓库验证。
