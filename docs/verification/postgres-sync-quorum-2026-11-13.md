# PostgreSQL 同步复制仲裁与脑裂防护验证记录（2026-11-13）

## 范围

在异步流复制多主机故障切换（见 `postgres-streaming-failover-2026-11-13.md`）
之外，补齐 `feature-coverage.md` 列出的「同步复制仲裁/脑裂场景」硬阻塞项：
在真实 1 主 2 从 `ANY 1` 同步仲裁拓扑上，验证同步提交耐久性、仲裁失速的
有界失败、真实网络分区下的自动提升与客户端故障切换、同步仲裁的自防护
（self-fencing）效应，以及防护缺失时的分歧写入与 `pg_rewind` 时间线重入。

本切片不改动任何生产 Go 代码：既有多主机 DSN、5 秒连接超时、10 秒
`statement_timeout` 与调用侧 context 截止已构成客户端边界；新增物仅为
测试代码、演练拓扑、演练脚本与文档。未配置仲裁时的默认路径与既有行为
逐字节一致。

## 交付内容

- `deploy/compose.ha-postgres-quorum.yaml`：1 主 2 从，静态 IP
  `172.31.81.10/11/12`，宿主 loopback 端口 15440-15442，独立命名卷；
  `wal_log_hints=on`（pg_rewind 前提）、`wal_keep_size=256MB`（保留
  分歧点之前的 WAL），备库经 `primary_conninfo` 固定 `application_name`。
- `backend/internal/storage/postgres/quorum_integration_test.go`：
  `TestQuorumStallBounded` 走生产 `Open()` 池，验证 SyncRep 等待被调用侧
  context 有界取消、歧义提交以同序列幂等重试（23505 即确认）、恢复后
  序列 1..N 连续无重复。
- `tooling/ha/postgres_quorum_drill.py` 与 22 项纯逻辑单测，纳入
  `make ha-tools-test` 门禁；覆盖仲裁 GUC 构造、LSN 解析、WAL 时间线
  解析、取消/重入 SQL 渲染、事件流评估等闭集。

## 演练中确认的四个关键语义

1. **引导死锁**：首启即带 `synchronous_standby_names` 会让建库事务等待
   尚不存在的备库，`CREATE DATABASE` 永久阻塞。拓扑因此先异步启动，待两个
   备库进入 streaming 后再 `ALTER SYSTEM ... pg_reload_conf()` 动态启用
   仲裁（SIGHUP 级，无需重启）。
2. **`statement_timeout` 不覆盖 SyncRep 等待**：提交发生在语句边界之后，
   零备库时提交挂在 `IPC/SyncRep`，10 秒 `statement_timeout` 不触发。
   真实边界是调用侧 context 截止：pgx 到期关闭连接，客户端在约 6 秒收到
   `context deadline exceeded`。单纯杀死客户端进程不会中断后端等待
   （SyncRep 等待不监听 socket 事件），后端会一直等到仲裁恢复。
3. **取消即歧义提交**：`pg_cancel_backend` 或客户端截止中断等待后，
   PostgreSQL 发 WARNING 并完成本地提交。服务器原文：
   `canceling wait for synchronous replication due to user request` /
   `The transaction has already committed locally, but might not have
   been replicated to the standby.` 客户端拿到错误但行已本地落盘，
   必须以幂等键重试（本切片以同序列 + 主键 23505 确认）。
4. **同步仲裁天然自防护**：网络分区后，被隔离的旧主仍保留
   `synchronous_standby_names`，任何写入都阻塞在 SyncRep 而无法完成，
   即仲裁配置本身阻止脑裂。只有显式放松该 GUC（模拟异步/误配部署）后，
   旧主才能产生分歧写入；`pg_rewind` 随后将其回卷到提升后的时间线并
   重新作为备库归队。

## 环境

- PostgreSQL 17.9（digest 锁定），Docker Compose v2，独立 bridge
  `172.31.81.0/24`；无 TLS、固定演练口令，`down -v` 清理命名卷。
- 仲裁：`synchronous_standby_names = 'ANY 1 (evia_standby_a,evia_standby_b)'`、
  `synchronous_commit=on`，两备库稳态 `sync_state=quorum`。
- 宿主经 bridge 路由直达容器 IP，Go 池与 psql 使用同一多主机 DSN。

## 真实演练结果

命令（工具链激活后）：

```
python3 tooling/ha/postgres_quorum_drill.py --duration 40
```

两轮独立运行均 `status=passed`、`topology_removed=true`，结束后容器、
网络、命名卷零残留：

| 阶段证据 | 第一轮 | 第二轮 |
| --- | --- | --- |
| 仲裁健康 | 两备库 `quorum`，GUC 一致 | 同左 |
| 同步耐久性 | 提交 LSN `0/40250A0`，两备库 flush LSN `0/40251A8` 均已超过 | 通过 |
| 失速窗口 Go 后端 | 采样到 `IPC/SyncRep` 阻塞后端 | 同左 |
| psql 探针阻塞到取消 | 7533ms，WARNING 后本地提交 | 8140ms，同一 WARNING |
| Go 池 context 截止 | 有界失败后幂等恢复 | 6001ms 截止 → 23505 确认 → 2568ms 恢复，`ambiguous_commits=1` |
| 分区自防护 | 隔离旧主写入阻塞，取消后记录歧义结果 | 同左 |
| 故障切换 RTO（分区到新主可写） | 37281ms | 37348ms |
| 提升后时间线 | WAL TLI 2 | WAL TLI 2 |
| victim 提交序列 | 151 成功 / 5 失败，1..151 连续无缺口 | 151 成功 / 5 失败，1..151 连续 |
| 独立交叉读取 | 新主 `151|151|151` | 同左 |
| pg_rewind 重入 | 分歧双序列清除，三节点 `151|151|151` 收敛于 TLI 2 | 同左 |

第二轮 Go 池原始事件流（摘要）：

```
{"event":"error","seq":17,"elapsed_ms":6001,"sqlstate":"","err":"timeout: context deadline exceeded"}
{"event":"error","seq":17,"elapsed_ms":1,"sqlstate":"23505", ... duplicate key ...}
{"event":"recovered","seq":18,"elapsed_ms":2568}
{"ambiguous_commits":1,"blocked_ms":6001,"committed_rows":18, ...}
```

## 时间线取证说明

提升后 `pg_control_checkpoint().timeline_id` 在负载下会短暂停留在旧值
（恢复结束检查点尚未写完），而 WAL 文件名前缀立即进入新时间线，故主库
侧以 `pg_walfile_name(pg_current_wal_lsn())` 前缀为准。备库恢复态不能
调用 `pg_walfile_name()`（报 `recovery is in progress`），改用
`pg_stat_wal_receiver.received_tli`（十六进制补齐为 8 位）轮询。

## 边界与诚实声明

- 单宿主容器拓扑，不验证跨故障域网络、TLS 与认证；明文 loopback 仅为
  演练资产，不是生产模板。
- `wal_keep_size=256MB` 在演练规模下保证 pg_rewind 可读回分歧前 WAL；
  生产应使用 WAL 归档（`restore_command`）或复制槽，不能依赖固定保留量。
- 仲裁自防护只阻止「无确认写入」；被取消的等待仍可能本地提交，应用写
  路径必须携带幂等键并把这类错误当歧义处理（Evia 既有写路径的
  `Idempotency-Key` 与预算池 `MaxRetries=-1` 正是该语义）。
- Evia 不执行提升与围栏；生产需 Patroni/repmgr 或托管控制面在恢复客户端
  流量前完成旧主隔离、回卷与重指，本演练只验证这些操作的数据层后果。
- 本证据为功能级仲裁/脑裂验证，不含生产容量矩阵与跨地域延迟下的
  `synchronous_commit` 取舍（`remote_apply`/`remote_write`/`on`）。
