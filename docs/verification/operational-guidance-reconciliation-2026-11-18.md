# 操作指引迁移上限与证据计数核销（operational guidance reconciliation）验证记录（2026-11-18）

## 问题（确定性缺口）

活文档中的部署/升级指引把 `evia migrate` 的目标写成早期编号（`011`/`012`），而迁移已推进到 PG `039`、CH `009`。新运维按文字执行会误以为迁移到历史编号即完成，属于会导向错误操作的现状性缺陷，与受保护的历史快照叙述性质不同。另有证据计数（集成测试 13→16）与机器授权上限（24→26）未随交付同步。

## 改动与依据

| 文件 | 改动 | 依据 |
| --- | --- | --- |
| `HANDOFF.md` 顶部表 | PG 迁移行更新为 `039` 并加「以 migrations 目录最新编号为准」自更新指针；CH 行更新为 `009`，显式标注 `006`–`009` 是属性读布局门控实验、禁止 backfill/生产切换/容量声明、生产仍走 v0 | 实际迁移目录；项目硬约束 |
| `HANDOFF.md` 新机步骤 5 | 「迁移至当前 `012`」改为执行 `evia migrate` 至内嵌最新编号（写作时 `039`） | `evia migrate` 应用全部内嵌迁移，命令本身版本无关 |
| `docs/machine-access.md` | 「migrate 至当前 `012`」改为版本无关表述 | 同上 |
| `docs/project-governance.md` | 「migrate 至 `011`」改为版本无关，保留 `011` 为治理能力落点 | 同上 |
| `docs/metrics.md` 部署段 | 「执行迁移 `012_metrics.sql`」改为全量 `evia migrate` 并注明 012 是指标落点；授权上限补注 012 当时 20→24、`016`/`020` 后当前为 26 | 迁移 `010`/`012`/`016`/`020` 的 cardinality CHECK 实测为 20/24/25/26 |
| `docs/feature-coverage.md` 行 31 | 集成资产测试计数 13→16，并注明含复合指标 helper 白名单正负例 | `python3 -m unittest discover -s integrations/tests` 实测 16；白名单仍为 9 文件 |

## 范围与语义决策

1. **只改现状性操作指引，不碰历史叙述**：`saved-analyses.md` 的「006 安装位应用 007/008」是当时升级路径的历史叙述，迁移顺序事实不变，保留；HANDOFF 的 SHA/CI/基线快照行与其第 256 行「不要把历史数字改成最新数字」的自我约定一致，不动。
2. **以自更新指针替代硬编码**：凡面向未来的迁移指引一律改为「以 migrations 目录最新编号为准」，消除每个切片都会复发的编号漂移缺陷。
3. **CH 实验布局不夸大**：`006`–`009` 在开发/验证环境经 glob 全量应用，但生产读路径仍为 v0，文档显式保留门控实验身份，不写成生产必需迁移。
4. **纯文档、无运行时代码面，不做变异**：以全量门禁证明无工程资产受影响。

## 验证

- `check_naming.py`、`verify_contracts.py` 通过；无契约或代码 diff，codegen 产物不变。
- 全量 `make check` EXIT=0、零 FAIL：Go 32 包 ok、集成资产 16 项、client 49/49、sdk 63/63、console 18/18、生产构建通过。日志：`/tmp/evia-check/operational-guidance-reconciliation/make-check.log`。

## 明确未做（边界）

- 不改任何迁移、代码或测试；不重写 HANDOFF/验证记录中的历史快照与 SHA。
- 不把 CH `006`–`009` 表述为生产就绪；属性布局生产切换仍受硬约束搁置。
- 邀请/注册（SMTP）、多机房 TLS、parquet/对象存储、TB 级容量与身份图等外部/决策阻塞项不在本切片。
