# PostgreSQL 流复制故障切换验证记录（2026-11-13）

## 范围

为全部 Evia 进程共用的 PostgreSQL 连接池补齐多主机故障切换能力：配置层
接受 libpq 风格的逗号分隔主机 DSN 与 `target_session_attrs=read-write`，
由 pgx 在连接建立时按序回退并拒绝仍处于恢复状态的备库；在真实的
1 主 1 备流复制容器拓扑上注入主节点故障，验证同一个长生命周期连接池
无需重启即可跟随提升后的新主，且提交序列连续、零数据丢失。

单主机 DSN（含本地开发使用的 `sslmode=disable`）行为保持逐字节不变。

## 交付内容

- `config.ValidateDatabaseURL`：DSN 闭集校验（1..8 主机、仅允许
  `sslmode` 六种 libpq 模式与 `target_session_attrs=read-write`、
  拒绝 fragment/转义路径/重复参数/未知参数），`ReadFor` 与
  migrate/bootstrap/account-create 管理命令路径同步接入。
- `internal/storage/postgres/database_failover_test.go`：无网络单测，
  证明通过校验的多主机 DSN 被 pgxpool 解析为有序回退主机并安装
  read-write 连接校验，默认单主机 DSN 不产生隐式会话属性。
- `failover_integration_test.go`（`integration` 构建标签）：基线写入
  与受害者循环，失败重试同一序号，最终断言提交序列恰为 1..N。
- `deploy/compose.ha-postgres.yaml`：静态 IP 桥接网络上的双节点拓扑，
  `pg_basebackup -R` 建立流复制，角色感知入口支持旧主清空重归。
- `tooling/ha/postgres_failover_drill.py`：纯标准库演练脚本，评估逻辑
  拆为无依赖纯函数并由 13 项单测覆盖，纳入 `make check`。

## 环境

- PostgreSQL 17.9（digest 锁定的 bookworm 镜像），Docker 20.10.24。
- 拓扑：`pg-primary`（172.31.78.10）、`pg-standby`（172.31.78.11），
  异步流复制，`wal_level=replica`、`wal_log_hints=on`，宿主经 Docker
  bridge 路由直达内部静态 IP。
- 演练 DSN：
  `postgres://evia:***@172.31.78.10:5432,172.31.78.11:5432/evia?sslmode=disable&target_session_attrs=read-write`
- 数据目录为命名卷，硬编码演练口令、无 TLS，演练结束 `down -v` 零残留。

## 真实演练结果

命令（工具链激活后）：

```
python3 tooling/ha/postgres_failover_drill.py
```

两轮独立运行均 `status=passed`：

| 指标 | 第一轮 | 第二轮 |
| --- | --- | --- |
| 故障前流复制状态 | streaming（1 个副本，LSN 对齐） | 同左 |
| `pg_promote` 后切换耗时 | 653 ms | 675 ms |
| 成功提交插入 | 350 | 349 |
| 故障窗口失败尝试 | 3 | 4 |
| 序列缺口 / 重复 | 0 / 0 | 0 / 0 |
| 独立 psql 交叉读取（count/distinct/max） | 350 / 350 / 350 | 349 / 349 / 349 |
| 旧主重归 | 以 standby 身份追平（replay LSN 非空） | 同左 |

证明的四个不变量：

1. **故障窗口写入失败而非误写备库**：主节点停止后插入立即报错，
   read-write 会话校验使连接不会落到恢复中的备库。
2. **同池自动恢复**：`pg_promote` 完成后，原连接池的新获取连接直接
   命中提升后的新主，受害者进程未重启、未重载配置。
3. **提交序列严格连续**：失败尝试保持原序号重试，最终提交集合恰为
   1..N，`generate_series` 缺口查询为 0，主键保证无重复。
4. **独立交叉读取一致**：经 `docker exec psql` 在新主上独立读取的
   count/distinct/max 与受害者摘要逐值相等；旧主可重新作为流复制
   备库接入并完成追平。

## 演练过程中暴露并修复的真实问题

1. Compose 把 command 块中的 shell `$VAR` 当主机插值吞掉 → 统一改写
   为 `$$` 转义（与 dev Redis 拓扑同类问题）。
2. 官方镜像默认 `pg_hba.conf` 没有 replication 连接规则，
   pg_basebackup 被拒 → 经 `configs` 挂载 initdb.d 脚本注入
   `host replication ... scram-sha-256`。
3. configs 最初误挂为 volumes 被 Compose 拒绝 → 改为服务级
   `configs:` 挂载并设置 0555 可执行位。
4. 旧卷残留导致入口脚本跳过初始化 → 演练固定 `down -v` 起步。
5. 演练脚本 `compose()` 未透传 `check`、基线 go test 缺少 `cwd` →
   均在首次真实运行时暴露并修复。
6. 缺口统计的 EXCEPT 标量子查询产生 NULL 扫描错误 → 拆为独立的
   `generate_series ... NOT IN` 查询。
7. 纯逻辑评估一度漏算 recovery 成功事件本身也是一次提交 → 修正序列
   重建逻辑并补回归单测。

## 门禁

- `make check` 退出码 0：含 13 项演练纯逻辑单测、config 闭集单测、
  pgxpool 构造单测、后端全套 `-race` 与前端构建。
- 真实容器演练两轮通过，结束后容器、网络、命名卷零残留。

## 边界与诚实声明

- 本验证是单宿主桥接网络上的功能级切换证据，使用异步复制与无 TLS
  口令认证，不构成跨可用区容灾或零丢失（RPO=0）结论；生产必须自行
  选择同步复制仲裁、受控提升（Patroni/repmgr/托管服务）与 TLS。
- 未覆盖网络分区（脑裂）、备库延迟超限、自动提升仲裁与连接风暴场景。
- ClickHouse Keeper 复制拓扑、Redis Cluster 与生产容量矩阵仍未验证。
