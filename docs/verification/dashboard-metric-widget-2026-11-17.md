# 看板直接指标版本组件验证记录（2026-11-17）

## 问题（确定性缺口）

看板组件（widget）此前只能固定不可变**保存分析版本**：领域 `DashboardWidget` 强制 `analysis_id`/`analysis_version`，关系见证 `dashboard_dependencies` 与 `dashboard_run_widgets` 的外键只指向 `saved_analysis_versions`，且 PL/pgSQL 形状函数 `dashboard_definition_valid` 与 deferred 完整性触发器都按 analysis 对校验。结果是一个已发布的独立指标无法直接放到看板上，必须先为它创建一份 metric_ref 保存分析再引用，多一层不可复用的包装。本切片让 widget 可二选一直接固定保存分析版本或不可变指标版本，覆盖后端、契约、真实数据库与运行扇出（控制台 UI 留后续切片）。

## 范围与语义决策

1. **二选一固定来源，不改变既有 analysis 路径**：widget 携带 `analysis_id`/`analysis_version` 或嵌套 `metric_ref`（`metric_id`+`version`），二者恰有一个；同时提供或都不提供在领域规范化、线网契约与数据库 CHECK 三层都被拒绝。既有 analysis-only JSON 逐字节兼容。
2. **双目标关系见证（迁移 034）**：`dashboard_dependencies` 与 `dashboard_run_widgets` 放宽 analysis 列为可空，新增 `metric_id`/`metric_version` 与 soundness CHECK；前者外键指向 `metric_versions`，后者外键指向前者的 metric 唯一约束（MATCH SIMPLE 下含 NULL 的行自动跳过，故旧 analysis 行无需回填）。
3. **不可变 PL/pgSQL 函数原地替换**：先 DROP 两个引用形状函数的 CHECK，再 `CREATE OR REPLACE` 函数，最后重建 CHECK；函数从嵌套 `metric_ref` 读 metric 标识（线网 `MetricReference` 的版本字段名为 `version`），deferred 触发器按 analysis 或 metric 两种键匹配见证。
4. **运行复用指标模板**：`dashboardRequestsTx` 对 metric widget 读取不可变 `metric_versions`，经既有 `MetricTemplate` 构建 trend 请求再 `Bind`，与直接指标运行共享项目查询预算，禁止隐式解析最新版本；运行读取用可空指针扫描两种来源并回填 `metric_ref`。
5. **环境与权限不放宽**：metric 版本必须与看板同项目/环境（否则 `DASHBOARD_METRIC_ENVIRONMENT_MISMATCH`），每个子查询仍按 `queries.run` 与原始数据访问规则复查授权，原子入队/整组回滚语义不变。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 迁移 | `034_dashboard_metric_widgets.sql` | 双目标外键、soundness/唯一约束、形状函数与 deferred 触发器重写 |
| 领域 | `reporting/dashboard.go`、`dashboard_policy.go` | widget/dependency/run 增加 `MetricRef`/metric 列；规范化二选一；校验匹配两类来源 |
| 存储 | `postgres/dashboard.go`、`dashboard_runs.go` | snapshot/publish/run/read 四路径 metric 分支与可空扫描 |
| 契约 | `openapi.json` | `DashboardWidget`/`DashboardDependency`/`DashboardWidgetRun` 增加 metric 字段、analysis 改可选、二选一描述；codegen 重生 |
| 测试 | `dashboard_metric_test.go`、`dashboard_metric_integration_test.go` | 领域二选一/匹配单测与真实 PG 混合看板集成 |

## 验证

### 领域单测（`internal/reporting`）

新增 `dashboard_metric_test.go`：混合来源定义规范化后保留 `metric_ref` 且不带 analysis 对；同时给两类来源、无来源、坏 metric UUID/版本四类畸形 widget 全部拒绝；`BuildDashboardValidation` 接受匹配的 metric 见证、拒绝仍带 analysis 列的错配见证。既有看板领域单测（布局、阅读顺序、diff、运行状态）零改动通过。

### 真实 PostgreSQL 集成（隔离 schema，`-tags=integration`）

新增 `TestDashboardMetricWidgetLifecycle`：发布一个 count 指标后，构建含一个 analysis widget 与一个 metric widget 的混合看板并发布；`dashboard_dependencies` 恰有 1 条 analysis 见证与 1 条 metric 见证；`EnqueueDashboardRun` 产生两个子查询，运行响应中 metric widget 带 `metric_ref` 且不含 analysis 对；引用不存在的 metric 版本在校验快照阶段失败；同时携带两类来源的 widget 在创建阶段被规范化拒绝。

### 迁移回归（最高风险点）

迁移 034 应用后，既有全套 `TestDashboard*`（含取消、运行时筛选、纳秒运行上下文、归档、审计回滚、固定快照）与 `TestSavedAnalysis*` 集成测试全部通过，证明形状函数替换、外键放宽与 deferred 触发器重写未改变 analysis-only 旧行为。

### 变异红绿（三轮，sha256 逐字节还原）

基线哈希记录于 `/tmp/evia-check/dashboard-metric-baseline.sha256`：

1. 运行路径强制走 analysis 分支（`if false && widget.MetricRef != nil`）→ 混合看板运行报空 UUID 语法错误，集成测试变红；
2. 领域二选一校验短路（`if false && hasAnalysis == hasMetric`）→ 无来源/坏 metric 引用被错误放行，领域单测变红；
3. 发布见证强制写 analysis 列（`if false && d.MetricID != ""`）→ metric 见证外键/类型违约，集成测试变红。

三轮均用 Edit 精确还原并 `sha256sum -c` 三个文件全部 OK，重跑单测与集成测试转绿。

### 门禁

- `go build ./...` 通过；`internal/reporting` 单测全绿；
- codegen 幂等、gofmt/命名检查无禁词；完整 `make check` 与全量 PG 集成包结果见提交时复核。

## 明确未做（边界）

- 控制台看板编辑器的「选择指标作为组件来源」UI 未实现，本切片为后端/契约/数据库能力，前端在后续切片接入；
- 看板文件夹、通用参数映射、比较期、跨请求结果缓存仍为独立缺口；
- metric widget 仍以指标自身固定口径生成 trend，不支持看板级覆盖聚合或新增参数；
- 不引入跨项目/跨环境引用，metric 与 analysis 一样受同一项目环境约束。
