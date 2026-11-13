# HA 与容量准备度套件验证记录（2026-11-13）

## 范围

HA 与容量工程此前交付了九个独立演练脚本，各自管理拓扑、环境变量与 JSON
报告。运维在发布前做准备度验收时需要逐个手动调用并手工汇总结果，缺少
单一可重复的准备度门禁。本切片交付统一套件编排器，把九个演练收敛为一条
命令，且不改变任何演练自身的逻辑。

本切片不改动生产 Go 代码与既有演练脚本；新增物为一个编排器与其纯逻辑
单测、文档。

## 交付内容

- `tooling/ha/ha_suite.py`：声明式注册表 + 前置评估 + 隔离子进程编排 +
  统一 JSON 报告。
- `tooling/ha/test_ha_suite.py`：18 项纯逻辑单测，纳入 `make ha-tools-test`
  （HA 工具累计 179 项）。

## 设计要点

1. **两类演练分层**：六个 `topology` 演练各自拥有独立 compose 工程并
   需要 Docker 守护进程；三个 `dev-stack` 演练（积压恢复、混合读写、
   长尾分布）复用运行中的测试容器，仅要求无默认值的 ClickHouse 凭据
   （URL 与 broker 在子脚本内有 loopback 默认值，故只登记为可选）。
2. **前置条件不产生副作用**：缺凭据或 Docker 不可用时标记 `skipped`
   并写明原因，而不是启动后崩溃；选择/前置/聚合全部为纯函数，可单测。
3. **失败隔离**：每个演练在独立子进程运行并带独立超时；单个失败不阻断
   其余演练，最终汇总 `failed` 计数。
4. **确定退出码**：全部通过/仅跳过返回 0；任一选中演练失败返回 1；
   注册表或选择参数错误返回 2，便于 CI 区分环境问题与断言失败。
5. **选择器**：`--only`/`--exclude` 按名称、`--tier` 按层、`--dry-run`
   只解析选择与前置不执行、`--report` 落盘机器可读报告。

## 真实验证

### 纯逻辑门禁

18 项单测覆盖注册表校验（重名/缺字段/坏 tier/坏超时）、名称与 tier
过滤组合、未知名称 fail-closed、环境变量缺失检测、Docker 前置、
跳过不计失败、未知状态拒绝聚合。

### dry-run 前置评估

- `--dry-run --tier topology`：6 个拓扑演练在 Docker 可用时全部通过前置、
  因 dry-run 标记 skipped；
- 移除 `EVIA_TEST_CLICKHOUSE_PASSWORD` 后，3 个 dev-stack 演练全部带
  「missing environment variables」原因跳过，证明前置不产生副作用。

### 端到端 dev-stack 套件

带真实凭据运行 `--tier dev-stack --report`，三个演练经统一编排顺序
执行并聚合为单一报告，全部 `passed`：

| 演练 | 结果 | 耗时 |
| --- | --- | --- |
| backlog-recovery | passed | 10769ms |
| mixed-read-write | passed | 6085ms |
| long-tail | passed | 3414ms |

汇总：`total=3, passed=3, failed=0, skipped=0, verdict=passed`，退出码 0；
运行后无残留主题或数据库。

### 退出码实测

- 未知演练名 `--only nope`：RC=2（配置错误）；
- `--dry-run` 全跳过：RC=0（跳过不构成失败）。

## 使用方式

```sh
# 只解析选择与前置条件，不执行
python3 tooling/ha/ha_suite.py --dry-run

# 运行复用测试容器的三个容量/流水线演练
EVIA_TEST_CLICKHOUSE_USERNAME=evia EVIA_TEST_CLICKHOUSE_PASSWORD=*** \
  python3 tooling/ha/ha_suite.py --tier dev-stack --report readiness.json

# 运行全部九个演练（拓扑演练会各自起停独立 compose 工程，耗时较长）
python3 tooling/ha/ha_suite.py --report readiness.json

# 只跑指定演练或排除某些演练
python3 tooling/ha/ha_suite.py --only redis-sentinel,redis-cluster
python3 tooling/ha/ha_suite.py --exclude postgres-quorum
```

## 边界与诚实声明

- 套件是编排层，不替代各演练自身的两轮真实容器证据；拓扑演练的正确性
  仍以各自 verification 文档为准，本切片端到端只实跑了 dev-stack 三子集
  （六个拓扑演练全套耗时约 30 分钟，其行为已由历史两轮证据覆盖）。
- 串行执行是刻意选择：拓扑演练各自独占 compose 工程与宿主端口，并行会
  引发资源与端口竞争；套件优化目标是可重复与可审计，不是缩短墙钟时间。
- 套件不负责安装依赖或启动测试容器；前置缺失即跳过并提示，环境准备仍
  遵循 development/dependencies 运行手册。
