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

除上一切片修复的机器权限外，**未发现新的内容漂移**；但发现三处闭集以内联字面量分散维护（discovery 能力清单、项目角色、配额词汇），先行重构为包级单一真相源，行为逐字节不变（仅常量替换，`go build`/既有测试全绿）。

## 常驻守卫（8 个测试，全部读嵌入 bundle、零外部依赖）

1. `access.TestMachinePermissionContractStaysAlignedWithClosedSet`（上一切片）：权限枚举 + 三 schema maxItems。
2. `access.TestProjectRoleContractStaysAligned`：三个 `ProjectMember*` schema 的 role 枚举 == `ProjectRoles`。
3. `reporting.TestDiscoveryVocabularyMatchesContract`：query kind/interval/保留粒度集合相等，funnel mode const 相等，主体子集，聚合枚举与近似聚合子集关系。
4. `reporting.TestFormulaAndComparisonVocabularyMatchesContract`：公式 7 算子、比较 5 单位集合相等。
5. `exporting.TestExportVocabularyMatchesContract`：三处格式枚举 + 五态作业状态。
6. `backfilling.TestBackfillStateMatchesContract`、`archiving.TestArchiveStatusMatchesContract`、`retention.TestSweepStatusMatchesContract`：各生命周期状态集合相等。
7. `diagnostics.TestDiagnosticsStatusMatchesContract`：四个分区状态均为 Go 闭集子集，pipeline 分区恰为完整四态且独占 no_data。
8. `control.TestCollectionQuotaVocabularyContract`：配额来源与运行态枚举相等。

## 守卫有效性取证（变异红绿）

- reporting：临时给 `discoveryQueryKinds` 注入 `drift_probe` → 守卫报 `query kind drift: contract 8 != Go 9` 变红，还原复绿。
- archiving：临时把 `drift_probe` 加入守卫期望集（模拟 Go 闭集单边扩项）→ 报 `archive status drift ... [drift_probe failed materialized pending]` 变红，还原复绿。
- 上一切片的机器权限守卫已用第 27 项注入验证过同类捕获力。
- 所有变异通过文件备份还原，`grep DriftProbe/drift_probe` 确认零残留。

## 验证

`go test` 覆盖 7 个包全部新守卫；全量 `make check` EXIT=0、零 FAIL（Go 32 包、client 51、sdk 63、console 18、生产构建）。日志：`/tmp/evia-check/contract-guards/make-check.log`。

## 明确未做（边界）

- 守卫只覆盖**枚举型闭集**；数值预算（31 天窗口、500 行等）仍由 `DiscoveryBudgets` 与契约描述分别维护，未机械比对——预算变更通常伴随语义评审，且部分预算仅存在于 Go 侧。
- `query-request.schema.json` 原始文件未单独 embed；守卫统一消费 OpenAPI 生成的 bundle 内联副本，codegen 漂移由 `generated-check` 门禁拦截。
- 不改变任何运行时行为、契约内容或 API 形状；本切片是可维护性/防回归工程。
