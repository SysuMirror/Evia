# 语义差异类别契约漂移修复（change category contract drift fix）验证记录（2026-11-19）

## 问题（确定性缺陷，两处）

在闭集契约对齐审计中对四类 `*Change.category` 枚举做 Go 产出侧比对，发现两个已交付能力的差异类别无法通过 HTTP 边界契约编码，与机器权限漂移同构：

1. **SavedAnalysisChange（缺 5 类）**：Go `savedAnalysisDiff` 会产出 `retention`/`segment`/`path`/`attribution`（2026-11-15 四类高级分析切片）与 `metric_ref`（指标引用切片），但 OpenAPI 枚举仍停留在 12 项原始查询块类别。
2. **DashboardChange（缺 1 类）**：看板参数切片（迁移 037）让 `dashboardDiff` 产出 `parameters`（板级参数与组件参数绑定两处），枚举仍为 5 项。

后果：对包含这些配置的已发布资源做**校验/发布**时，`Boundary.Encode` 会因未知枚举拒绝整个 `*Validation` 响应，表现为 500；transport 测试的 service mock 固定返回空 Diff，所以长期未暴露。

## 先红后绿

- 新增 `TestSavedAnalysisChangeCategoriesSatisfyContract`：对五个漂移类别逐一走 `contract.Decode` + `Validate("SavedAnalysisChange")`，修复前实测报 `jsonschema validation failed` 红灯。
- 修复后同测试转绿；另对看板 `parameters` 由全量类别守卫覆盖。

## 修复与防复发

### 契约（无迁移、纯枚举扩展）
- `SavedAnalysisChange.category`：12 → 17（补 retention/segment/path/attribution/metric_ref）。
- `DashboardChange.category`：5 → 6（补 parameters）。
- codegen 三步同步 Go wire、离线 bundle 与 TS 类型；`verify_contracts.py` 通过。纯枚举加宽，对既有消费方向后兼容。

### Go 单一真相源
新建 `reporting/change_categories.go`：23 个 `ChangeCategory*` 常量 + 四套类别切片（保存分析 17、原子指标 7、看板 6、复合公式 3），替换四个 diff 构造器中的全部内联字面量（含误拼修复后二次核对编译）。行为仅常量替换，输出字符串不变。

### 常驻守卫（reporting 包）
- `TestChangeCategoryVocabularyMatchesContract`：四套切片与 bundle 内四个 `*Change.category` 枚举做集合相等断言。
- `TestEveryChangeCategoryEncodesThroughBoundary`：每个类别经严格 JSON 解码后过对应 schema，证明边界可编码而非纸面相等。
- 变异取证：向看板切片注入 `drift_probe` → 守卫报 `DashboardChange category drift ... != Go [...]` 变红，文件备份还原后复绿且字节一致。

### 控制台
发布评审组件的类别标签表补齐五项保存分析类别（留存分析/主体分群/聚合路径/触点归因/指标引用）与看板 `parameters`（运行参数）；组件本就有 `?? change.category` 回退，枚举加宽不会导致渲染崩溃。

## 验证

- reporting 包全套单测通过；真实 PG `TestSavedAnalysis|TestMetric|TestDashboard|TestFormulaMetric` 集成回归通过（46s），证明 diff 构造行为不变。
- `pnpm typecheck` 通过；全量 `make check` EXIT=0、零 FAIL（Go 32 包、client 51、sdk 63、console 18、生产构建）。

## 明确未做（边界）

- 不改变 diff 路径（`path` 字段）词汇与差异计算语义，仅修复类别枚举覆盖。
- 原子指标（7 类）与复合公式（3 类）枚举经审计本就对齐，未改契约。
- 数值型差异（before/after 内容）的展示仍由既有评审表格负责，本切片不新增 UI 形态。
