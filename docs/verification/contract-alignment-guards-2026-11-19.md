# 闭集契约对齐守卫体系（contract alignment guards）验证记录（2026-11-19）

## 问题（确定性缺口）

修复机器权限枚举漂移（`data.erasure`/`data.export` 无法经正式 API 签发，见 [machine-governance-grant-contract-2026-11-19](machine-governance-grant-contract-2026-11-19.md)）后做全仓枚举审计：Go 闭集与 OpenAPI 枚举分散维护，除机器权限外没有任何机械对齐保证，同类漂移可能潜伏在其他词汇表。

## 审计方法与结论

机械提取嵌入 bundle（`wire.Schemas`，由 OpenAPI 离线生成）的全部字符串枚举，与 Go 侧常量/校验闭集逐项比对：

| 闭集 | Go 真相源 | 契约位置 | 结论 |
| --- | --- | --- | --- |
| 机器权限（26） | `access.machinePermissions` | `MachinePermission.enum` | 上一切片已修复并加守卫 |
| 项目角色（3） | 内联字面量 → 提取为 `access.RoleOwner/Editor/Viewer` + `ProjectRoles` | `ProjectMember*.role.enum` | 对齐 |
| 查询种类（8）/间隔（4）/聚合（10）/保留粒度（3）/漏斗模式（1）/主体 | discovery 内联切片 → 提取为 `reporting.discovery*` 包级变量 | `QueryRequest`（bundle 内联） | 对齐；`resolved_person` 为契约保留项，discovery 刻意只宣告已实现主体（子集关系） |
| 公式算子（7）/比较单位（5） | `reporting.Formula*` / `Comparison*` 常量 | `FormulaExpression.op` / `ComparisonSpec.unit` | 对齐 |
| 导出格式（3）/作业状态（5） | `exporting.Format*` / `State*` | `ExportInput`/`ExportJob`/`SavedAnalysisExportInput` | 对齐 |
| 回补状态（5） | `backfilling.State*` | `BackfillJob.state` | 对齐 |
| 归档状态（3） | `archiving.Status*` | `ArchiveManifest.status` | 对齐 |
| 清理状态（3） | `retention.Sweep*` | `RetentionSweep.status` | 对齐 |
| 诊断状态（4，仅 pipeline 可用 no_data） | `diagnostics.Status*` 常量 | 四个 `Diagnostics*.status` | 对齐；守卫同时锁定 no_data 的分区适用性 |
| 配额来源（2）/运行态（2） | control 内联字面量 → 提取为 `quota*` 常量 | `CollectionQuota.source/runtime_status` | 对齐 |
| 依赖选择模式（8） | 14 处内联字面量 → 提取为 `reporting.SelectionMode*` 常量 + `SelectionModes` | `QueryDependencyManifest.selection_mode` | 对齐 |
| 看板参数类型（3）/算子（9） | 算子内联 map → `ParameterOp*` 常量 + `ParameterTypes/ParameterOperators` | `ParameterDeclaration.type/operator` | 对齐 |
| 可视化类别（5） | switch 内联字面量 → `Visualization*` 常量 + `VisualizationKinds` | `SavedAnalysisVisualization.kind` | 对齐 |
| 看板运行聚合态（7） | `DashboardRunState` 内联返回值 → `DashboardRun*` 常量 + `DashboardRunStates`（聚合读取的作业态抽为包内 key 常量） | `DashboardRun.state` | 对齐 |
| 公式计量单位（8）/换算目标对（2） | 单位 switch 内联字面量 → `reporting.Unit*` 常量 + `KnownUnits`，换算表改用常量键 | `MetricDefinition.unit` / `FormulaDefinition.unit` / `FormulaExpression.target_unit` | 对齐 |
| 查询作业态（6） | 五包 20+ 文件内联字面量 → `analysis.JobState*` 常量 + `JobStates`（独立切片，见 [queryjob-state-closed-set](queryjob-state-closed-set-2026-11-19.md)） | `QueryJob.state` | 对齐 |
| 部署环境（3，54 个 schema 属性） | 27 个生产文件四种重复形态 → `platform/env` 叶子包 `Development/Staging/Production` + `Environments/Valid`（独立切片，见 [environment-closed-set](environment-closed-set-2026-11-19.md)） | 全部 `environment`/`target_environment` 枚举 | 对齐 |
| 归因模型（5） | validation/attribution/planner 内联字面量 → `analysis.AttributionModel*` 常量 + `AttributionModels/ValidAttributionModel`（独立切片，见 [attribution-model-closed-set](attribution-model-closed-set-2026-11-19.md)） | `QueryRequest.attribution.model` | 对齐 |
| 采集来源 kind（2）/status（2） | 10 个生产文件内联字面量 → `ingestion.SourceKind*/SourceStatus*` 常量 + 校验助手（独立切片，见 [source-vocabulary-closed-set](source-vocabulary-closed-set-2026-11-19.md)） | `Source*` 四 schema 的 6 个枚举属性 | 对齐 |
| 事件事实类型（3） | validation 长布尔链内联字面量 → `catalog.FactKind*` 常量 + `ValidFactKind`（独立切片，见 [fact-kind-closed-set](fact-kind-closed-set-2026-11-19.md)） | `DraftInput.fact_kind` | 对齐 |
| 查询结果精度（3） | planner/validation/公式对齐器内联字面量 → `analysis.Accuracy*` 常量 + `AccuracyValues/ValidAccuracy`（独立切片，见 [accuracy-closed-set](accuracy-closed-set-2026-11-19.md)） | `QueryResult.metadata.accuracy` | 对齐 |
| 兼容性判定（3）/指标引用类型（2） | compatibility.go 8 个裸 return → `catalog.Compatibility*`；MetricRef 补 `MetricRefKinds` 切片（独立切片，见 [compat-refkind-closed-set](compat-refkind-closed-set-2026-11-19.md)） | `ValidationResult.compatibility` / `MetricReference.kind` | 对齐 |
| 查询主体（2+预留 1） | validation/planner/metric_policy 六处重复闭集判断 → `analysis.Subject*` 常量 + `Subjects/ValidSubject`（独立切片，见 [subject-closed-set](subject-closed-set-2026-11-19.md)） | `MetricDefinition.subject` / `QueryRequest.subject` | 对齐 |

除上一切片修复的机器权限外，**未发现新的内容漂移**；但发现三处闭集以内联字面量分散维护（discovery 能力清单、项目角色、配额词汇），先行重构为包级单一真相源，行为逐字节不变（仅常量替换，`go build`/既有测试全绿）。

## 常驻守卫（18 个测试，全部读嵌入 bundle、零外部依赖）

1. `access.TestMachinePermissionContractStaysAlignedWithClosedSet`（上一切片）：权限枚举 + 三 schema maxItems。
2. `access.TestProjectRoleContractStaysAligned`：三个 `ProjectMember*` schema 的 role 枚举 == `ProjectRoles`。
3. `reporting.TestDiscoveryVocabularyMatchesContract`：query kind/interval/保留粒度集合相等，funnel mode const 相等，主体子集，聚合枚举与近似聚合子集关系。
4. `reporting.TestFormulaAndComparisonVocabularyMatchesContract`：公式 7 算子、比较 5 单位集合相等。
5. `exporting.TestExportVocabularyMatchesContract`：三处格式枚举 + 五态作业状态。
6. `backfilling.TestBackfillStateMatchesContract`、`archiving.TestArchiveStatusMatchesContract`、`retention.TestSweepStatusMatchesContract`：各生命周期状态集合相等。
7. `diagnostics.TestDiagnosticsStatusMatchesContract`：四个分区状态均为 Go 闭集子集，pipeline 分区恰为完整四态且独占 no_data。
8. `control.TestCollectionQuotaVocabularyContract`：配额来源与运行态枚举相等。
9. `reporting.TestFormulaUnitVocabularyMatchesContract`：`MetricDefinition`/`FormulaDefinition` 的 unit 枚举与 `KnownUnits` 集合相等，`FormulaExpression.target_unit` 恰为毫秒/秒换算目标对。
10. `analysis.TestQueryJobStateVocabularyMatchesContract`：`QueryJob.state` 六态枚举与 `analysis.JobStates` 集合相等；存储侧副本由迁移 005 的 CHECK 约束兜底。
11. `env.TestEnvironmentVocabularyMatchesEveryContractSchema`：递归遍历全部 `$defs`，54 个 `environment`/`target_environment` 枚举属性与 `env.Environments` 集合相等，并设覆盖下限防 schema 重构缩水。
12. `analysis.TestAttributionModelVocabularyMatchesContract`：嵌套 `QueryRequest.attribution.model` 枚举与 `AttributionModels` 集合相等。
13. `ingestion.TestSourceVocabularyMatchesContract`：四个 Source schema 的 kind×3/status×3 枚举与 `SourceKinds`/`SourceStatuses` 集合相等，并锁定 3+3 覆盖数。
14. `catalog.TestFactKindVocabularyMatchesContract`：`DraftInput.fact_kind` 枚举与 `FactKinds` 集合相等。
15. `analysis.TestAccuracyVocabularyMatchesContract`：嵌套 `QueryResult.metadata.accuracy` 枚举与 `AccuracyValues` 集合相等。
16. `catalog.TestCompatibilityVocabularyMatchesContract`：`ValidationResult.compatibility` 枚举与 `CompatibilityValues` 集合相等。
17. `reporting.TestMetricReferenceKindVocabularyMatchesContract`：`MetricReference.kind` 枚举与 `MetricRefKinds` 集合相等。
18. `analysis.TestSubjectVocabularyMatchesContract`：`MetricDefinition.subject` 与 `Subjects` 集合相等，`QueryRequest.subject` 恰为 `Subjects` 加预留值 `resolved_person`。

## 守卫有效性取证（变异红绿）

- reporting：临时给 `discoveryQueryKinds` 注入 `drift_probe` → 守卫报 `query kind drift: contract 8 != Go 9` 变红，还原复绿。
- archiving：临时把 `drift_probe` 加入守卫期望集（模拟 Go 闭集单边扩项）→ 报 `archive status drift ... [drift_probe failed materialized pending]` 变红，还原复绿。
- 上一切片的机器权限守卫已用第 27 项注入验证过同类捕获力。
- 公式单位：向 `KnownUnits` 注入 `drift_probe` → 报 `metric unit drift ... Go 9 != contract 8` 变红；向守卫的换算目标期望集注入 `UnitByte` → 报 `convert target unit drift ... [byte millisecond second]` 变红，两条断言分支均被证有效。
- 查询作业态：向 `analysis.JobStates` 注入 `drift_probe` → 报 `query job state drift ... Go 7 != contract 6` 变红，还原复绿（详见独立验证记录）。
- 部署环境：向 `env.Environments` 注入 `drift_probe` → 首个 schema 即报 `environment drift ... Go 4 != contract 3` 变红；抬高覆盖下限 → 报 `only checked 54` 变红，还原复绿（详见独立验证记录）。
- 归因模型：向 `AttributionModels` 注入 `drift_probe` → 报 `attribution model drift ... Go 6 != contract 5` 变红，还原复绿（详见独立验证记录）。
- 采集来源词汇：分别向 `SourceKinds`/`SourceStatuses` 注入 `drift_probe` → kind/status 两条断言分支均报 drift 变红，还原复绿（详见独立验证记录）。
- 事件事实类型：向 `FactKinds` 注入 `drift_probe` → 报 `fact kind drift ... Go 4 != contract 3` 变红，还原复绿（详见独立验证记录）。
- 查询精度：向 `AccuracyValues` 注入 `drift_probe` → 报 `accuracy drift ... Go 4 != contract 3` 变红，还原复绿（详见独立验证记录）。
- 兼容性/指标引用：分别向 `CompatibilityValues`、`MetricRefKinds` 注入 `drift_probe` → 两个守卫均报 drift 变红，还原复绿（详见独立验证记录）。
- 查询主体：向 `Subjects` 注入 `drift_probe`（metric 分支变红）、把预留常量改为 `drift_probe`（query 分支变红），双分支独立验证后还原复绿（详见独立验证记录）。
- 所有变异通过文件备份还原，`grep DriftProbe/drift_probe` 确认零残留。

## 验证

`go test` 覆盖 7 个包全部新守卫；全量 `make check` EXIT=0、零 FAIL（Go 32 包、client 51、sdk 63、console 18、生产构建）。日志：`/tmp/evia-check/contract-guards/make-check.log`、`/tmp/evia-check/formula-units/make-check.log`（公式单位切片第二轮全量门禁）。

## 明确未做（边界）

- 守卫只覆盖**枚举型闭集**；数值预算（31 天窗口、500 行等）仍由 `DiscoveryBudgets` 与契约描述分别维护，未机械比对——预算变更通常伴随语义评审，且部分预算仅存在于 Go 侧。
- `query-request.schema.json` 原始文件未单独 embed；守卫统一消费 OpenAPI 生成的 bundle 内联副本，codegen 漂移由 `generated-check` 门禁拦截。
- 不改变任何运行时行为、契约内容或 API 形状；本切片是可维护性/防回归工程。
- ~~`QueryJob.state` 六态提取~~ 已完成（2026-11-19 独立切片）：常量落在 `analysis.JobState*`，raw SQL/迁移字面量刻意保留，由 DB CHECK 与契约守卫双侧兜底，详见 [queryjob-state-closed-set](queryjob-state-closed-set-2026-11-19.md)。
