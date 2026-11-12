# 备份恢复自动调度垂直切片验证记录（2026-11-12）

## 范围

`tooling/backup/schedule.py` 周期编排器（配置校验、flock 互斥、PG/CH 演练编排、保留期清理、跨重启状态累积、node_exporter textfile 指标原子写出）、systemd service/timer、textfile 指标清单与监控门禁交叉校验、备份失败/陈旧告警及 promtool 实测。

## 自动化测试证据

- `python3 -m unittest discover -s tooling/backup`：54 项全绿（新增 `test_schedule.py` 13 项，覆盖配置边界、flock 重叠拒绝、计数器累积、fail-closed 与密钥脱敏、仅托管文件参与裁剪、textfile 渲染顺序）。
- `tooling/test_check_monitoring.py`：14 项全绿，新增 textfile 清单与 `schedule.py` 发射端双向漂移检测、畸形清单拒绝。
- `python3 tooling/check_monitoring.py`：真实规则文件通过，新增 `evia_backup_*` 指标只能经清单进入允许列表。
- `promtool test rules`：SUCCESS，新增 4 个告警族用例（失败触发/健康静默/26h 陈旧触发/近期成功静默）。

联调中修复三处真实缺陷：

1. CH 标识非法值抛 `ValueError` 未转换为调度器 `ScheduleError`。
2. PG 密钥脱敏误用整条 DSN 而非其中密码，失败报告可能泄露口令。
3. `Path("")` 退化为当前目录绕过空路径校验；以及 CH 备份名正则误加 `z` 后缀，导致真实产物永远匹配不上、保留期裁剪失效（单测夹具用了同样错误的名字，真实容器运行才暴露）。
4. PG `do_drill(keep=True)` 会保留 scratch 数据库；调度器改为 `keep=False`（dump/manifest 本就始终保留），scratch 保证清理。

## 真实容器证据

对开发栈真实容器（PostgreSQL 17 on 55432、ClickHouse on 18123）执行四次成功 `run-once` 加一次负例：

- 双目标演练全部 `ok=true`：PG 走完整 pg_dump → scratch 库 → pg_restore → 行数/内容哈希/迁移账本三轴校验；CH 对静态库 `evia_schedule_drill`（1 表 3 行固定数据，规避活库 `pipeline_readiness` 持续写入造成的快照漂移）执行原生 BACKUP → tar 导出 → scratch 恢复 → 指纹比对。
- 连续运行后状态累积正确：`success_total=3`、`failure_total=0`、`evia_backup_up=1`，时间戳与持续时间逐次更新。
- 保留期 `retention_keep=2` 实测生效：第四次运行后 PG 仅保留最新 2 组 dump/manifest 与 2 份报告，CH 仅保留最新 2 个备份目录与清单，裁剪动作逐条记录在 run report 的 `pruned` 字段。
- scratch 零残留：每次运行后 `evia_restore_drill_*`（PG）与 `evia_ch_restore_drill_*`（CH）计数均为 0。
- textfile 指标文档包含全部 6 个指标的 HELP/TYPE 与按 target 排序的标签序列，原子替换可直接被 node_exporter textfile collector 抓取。
- 负例（指向不存在的数据库）：进程退出码 1，PG `evia_backup_up=0`、`failure_total=1`，CH 仍成功且 `up=1`，证明单目标失败不阻断其余目标且 fail-closed；错误信息经报告留存而口令不出现。

## 边界与诚实声明

- 开发栈 PostgreSQL 容器内无宿主客户端工具链，本次证据通过本地 docker-exec shim 转发 `psql`/`pg_dump`/`pg_restore`（与既有 runbook 记录的方式一致）；生产部署按 runbook 在宿主直接安装客户端，调度器命令行接口不变。
- 活 ClickHouse 库在演练窗口内持续写入会被指纹比对正确判为漂移，这是工具的预期行为；生产应对稳定副本或低写入窗口执行周期演练。
- systemd 单元为部署资产，未在当前容器环境启动；其行为由调度器单测与 runbook 安装步骤保证。
