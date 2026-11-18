# 活文档能力声明准确性核销（documentation accuracy reconciliation）验证记录（2026-11-18）

## 问题（确定性缺口）

多轮垂直切片交付后，若干活文档仍停留在早期能力快照，出现与当前代码相反的能力声明，机器与人类接入方据此会得到错误结论。本切片只核销事实性错误，不改任何运行时代码、契约或测试。

## 逐条核销与代码依据

| 文档 | 过时声明 | 代码核实后的事实 |
| --- | --- | --- |
| `docs/metrics.md` 开头段 | 公式接口「尚未接通」、单位换算与看板引用「仍待交付」 | 复合指标全链路已交付（迁移 `013`/`014`/`033`，`/formula-metrics` 全套路由与控制台）；`convert` 节点与迁移 `034` 看板指标组件均已交付，改为指向下文专章 |
| `docs/metrics.md` 统计口径 | 「不会把毫秒转换成秒」（无歧义地读成任何情况下都不能） | 隐式/自动换算仍不存在，但复合指标有显式封闭 `convert`；改为区分隐式与显式两条路径 |
| `docs/metrics.md` | 「系统事件运行时注册及身份图仍未实现」 | 系统事件运行时目录已交付（embed 目录、采集器 `sys.` 旁路、公开配置）；仅身份图仍未实现，拆成两句分别陈述 |
| `docs/metrics.md` 复合章节/末段 | 看板直接引用「仍待交付」、粒度仅 `minute/hour/day` | 迁移 `034` 已交付看板指标组件；`validation.go:167` 闭集含 `week`，粒度补为 `minute/hour/day/week` |
| `docs/project-governance.md` | 账号恢复/停用、机器权限、配额、审计生命周期「仍待交付」 | `account-disable/enable/reset-password/audit` 运维命令、迁移 `028` 审计、机器访问与配额控制台均已交付；仅邀请/注册自助因 SMTP 缺失仍缺 |
| `docs/reporting-discovery.md` | 「independent metrics remain planned」 | 原子与复合指标均已是独立版本化资源，改为已交付表述 |
| `docs/saved-analyses.md`、`docs/dashboards.md` | 指标引用面未区分原子/复合，易被读成复合也可引用 | 三路（保存、看板保存/复制/运行）解析都只调 `readMetricVersionTx` 读 `metric_versions`，复合 ID 无行即失败；显式标注仅原子可引用、复合引用未接通 |
| `docs/feature-coverage.md` 行 28 | 状态标签限定语停在「原子指标前后端已实现」 | 正文已记录公式/嵌套/换算/归档/搜索/百分位全交付，标签更新为真实剩余边界「复合指标的保存分析/看板复用未接通」，保留「部分」等级不翻转 |

### 过程中纠正的一处过度核销

初稿曾把保存分析/看板的指标引用写成「原子或复合」。逐路核实存储解析（`reporting_saved.go:140`、`dashboard.go:312`、`dashboard_copy.go:140`、`dashboard_runs.go:444/498`）后确认全部只查原子表 `metric_versions`，不存在 `formula_metric_versions` 分支，随即在 `metrics.md`、`dashboards.md`、`saved-analyses.md` 与 `integrations/README.md` 统一回退为「仅原子、复合未接通」。该边界同时被本切片写进 feature-coverage 标签，防止后续再次误读。

## 范围与语义决策

1. **只改活文档，不动验证记录**：`docs/verification/` 下是时点证据，即使表述陈旧也保持原样，不重写历史。
2. **保守保留真实未完成项**：看板硬删除、通用参数绑定、跨请求结果缓存、Flink、跨文档会话连续性、parquet/对象存储、SMTP 邀请注册等经验证确未交付的声明一律保留，不把「部分」粉饰成「完成」。
3. **无运行时代码面，不做变异**：与契约自描述切片同口径，以「逐条代码核实 + 全量门禁」证明文档变更不破坏任何工程资产；Markdown 不进入 prettier/Go/TS 编译面。

## 改动清单

`docs/metrics.md`（6 处）、`docs/project-governance.md`、`docs/reporting-discovery.md`、`docs/saved-analyses.md`、`docs/dashboards.md`、`docs/feature-coverage.md`（状态标签 1 处）、`integrations/README.md`（纠正上一切片的原子/复合表述），共 7 个文件、纯文档增删。

## 验证

- `check_naming.py`、`verify_contracts.py` 通过；文档变更不影响 codegen 产物（无契约 diff）。
- 全量 `make check` EXIT=0、零 FAIL：Go 32 包 ok、client 49/49、sdk 63/63、console 18/18、生产构建通过。日志：`/tmp/evia-check/docs-accuracy-reconciliation/make-check.log`。

## 明确未做（边界）

- 不实现复合指标的保存分析/看板引用、看板硬删除或通用参数绑定——它们是功能工作包，不是文档问题。
- 不改任何验证记录的历史结论；不翻转 feature-coverage 的「部分」等级。
- 漏斗严格有序、身份关联图、来源系统字段分析化、热力图/会话回放与外部基础设施仍待单独拍板或外部环境。
