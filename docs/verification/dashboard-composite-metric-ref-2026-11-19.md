# 看板复合公式指标组件（dashboard composite formula metric widget）验证记录（2026-11-19）

## 问题（确定性缺口）

迁移 `034` 的看板指标组件只能通过 `metric_versions` 外键固定原子指标；上一切片打通保存分析复合引用后，feature-coverage 行 28 的唯一剩余边界就是「复合指标的看板组件引用未接通」。本切片把见证表从双目标扩为三目标并打通运行/复制全链路。

## 设计决策

沿用迁移 `033`/`034` 的「通用身份列 + 可空双外键 + soundness CHECK」模式扩为三目标，不新增表、不改变既有行：`analysis XOR atomic metric XOR composite formula` 恰一非空。复合选择仍由 `metric_ref.kind` 显式判别（Option A，与保存分析切片一致），空 kind 保持原子 JSON 字节不变。

## 改动清单

### 迁移 `040_dashboard_formula_metric_refs.sql`（无 backfill）
- `dashboard_dependencies`：动态发现并替换原子外键，新增 `formula_metric_id/formula_metric_version`，分别外键到 `metric_versions` 与 `formula_metric_versions`；`source_sound` CHECK 升级为三选一；新增 formula 唯一约束。
- `dashboard_run_widgets`：同构三目标，外键指向依赖见证的对应列。
- 重写延迟触发器 `dashboard_check_dependencies`：清单匹配与完整性校验都按 `metric_ref.kind='composite'` 路由到 formula 列；文本卡仍无见证。
- 形状函数 `dashboard_definition_valid` 无需改动：has_metric 对两类 metric_ref 均成立，kind 闭集由 Go 归一化权威校验。

### Go 领域（`reporting`）
- `DashboardDependency` 增加 `FormulaMetricID/FormulaMetricVersion`；`BuildDashboardValidation` 依赖匹配加 formula 分支。
- `NormalizeDashboardDefinition` 移除上一切片的复合拒绝，保留 kind 闭集校验（只校验不写回默认值）。
- `RebindDashboardDefinition` 重绑定保留源组件 kind，跨环境复制不丢失复合身份。

### Go 存储（`postgres`）
- 依赖构建/插入：复合组件读 `formula_metric_versions` 并写 formula 见证列。
- 运行：新增 `dashboardFormulaMarkerRequestTx`，复合组件构造带 `analysis.FormulaJobRef` 的标记请求（镜像公式直跑的 31 天/IANA 闭集），当前窗口与比较期两路共用，由 query worker 的 `CompositeFormulaEngine` 展开 DAG；运行组件插入/扫描/读回三目标化，读回的 `MetricRef` 带 `kind=composite`。
- 跨环境复制：`assertRebindingTargetsTx` 按源组件 kind 选择版本表。

### 控制台
- 看板组件选择器新增「已发布复合公式指标」来源，独立列表/版本查询；编辑器对 formula 来源写 `kind:'composite'`，原子来源保持无 kind。
- 来源标签区分「复合指标 vN / 指标 vN」；跨环境重绑定载荷保持无 kind（服务端保留源 kind）。

## 验证

### 真实 PostgreSQL 集成（隔离 schema + 全量迁移到 040）
新增 `TestDashboardCompositeFormulaWidgetLifecycle`：发布复合指标→建复合看板→断言见证只落 formula 列→运行断言作业携带 formula_ref 标记、读回 kind=composite、run_widgets 走 formula 外键→缺失版本 fail-closed→全 NULL 身份 run widget 被 soundness CHECK 拒绝。
回归 `TestDashboard|TestSavedAnalysis|TestFormulaMetric` 整组通过（34s），含既有原子看板、比较期、文本卡、跨环境复制。

### 变异红绿（基线 `/tmp/evia-check/dashboard-composite-ref/fe/SHA256SUMS`）
1. 运行标记 `Formula` 置 nil → 生命周期测试红（作业缺 formula_ref），还原 sha256 一致复绿。
2. run widget `source_sound` 放宽为 `CHECK(true)` → soundness 断言红（全 NULL 身份行被放行），还原复绿。
   - 取证过程发现初版用例在依赖表上被完整性触发器抢先拦截、无法区分 CHECK 贡献，改为在无触发器的 run_widgets 表用 MATCH SIMPLE 全 NULL 行精确打击 CHECK 独有守卫。

### 前端与门禁
`tsc --noEmit` 通过；Playwright model 看板规格 9/9（含新增复合用例）；全量 `make check` EXIT=0、零 FAIL（Go 32 包、client 49、sdk 63、console 18、生产构建）。日志：`/tmp/evia-check/dashboard-composite-ref/make-check.log`。

## 明确未做（边界）

- 不支持复合组件上的运行时参数绑定/分组等原子高级能力之外的新形态；复合仍以整段标记卡片运行。
- 漏斗严格有序、身份关联图、来源系统字段分析化、热力图/会话回放与外部基础设施仍待单独拍板或外部环境。
