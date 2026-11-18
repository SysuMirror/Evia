# 字符串后缀筛选（suffix / not_suffix）验证记录（2026-11-18）

## 问题（确定性缺口）

字符串筛选有 `prefix`（startsWith）与 `contains`（position）及其否定，却没有「后缀匹配」。调用方无法表达「以 .csv 结尾」「邮箱域名后缀」等条件。本切片补齐对称的 `suffix`/`not_suffix`，与 prefix 同属字符串标量算子，不引入新查询类型或资源模型。

## 范围与语义决策

1. **标准 endsWith，无开放选择**：planner 对 `suffix` 渲染 `endsWith(value,x)`、`not_suffix` 渲染 `NOT endsWith(value,x)`，函数在真实 ClickHouse 26.8 已验证（匹配/不匹配/否定三态正确）。
2. **守卫-反转，沿用既有不变量**：两个算子都编译为 `(scope AND present AND valid AND 内层比较)`，否定只反转内层 `endsWith`，绝不把缺失键、显式 null、非字符串类型折叠进结果——与 `neq`/`not_prefix` 同一三值逻辑约定，不提供通用 `not` 节点。
3. **仅字符串标量**：`suffix`/`not_suffix` 接受标量值、传数组按既有值形状规则拒绝；`CheckResolved` 限定 string 类型，作用于数值/数组字段拒绝；数组字段不新增后缀算子（数组只有 contains/not_contains）。
4. **fail-closed 闭集不削弱**：未知算子仍以 `INVALID_FILTER_OPERATOR` 拒绝；契约枚举、Go 离线 bundle、TS 联合类型经 codegen 三处一致。
5. **边界**：看板参数的每类型算子白名单是独立闭集，本次不动（与筛选否定切片同一显式边界）；用户值仍走绑定参数，不进入 SQL 文本。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 契约 | `contracts/query/query-request.schema.json` | 比较过滤 op 枚举加 `suffix`/`not_suffix`；codegen 同步 Go bundle 与 TS 两处联合类型（openapi 经外部 `$ref` 跟随） |
| 领域校验 | `internal/analysis/validation.go` | 算子闭集加两项（值形状复用标量规则） |
| 类型规则 | `internal/analysis/fields.go` | 文本算子 string-only 检查纳入后缀族 |
| 计划器 | `internal/analysis/planner.go` | `endsWith` 与 `NOT endsWith` 分支，保留同一 guard |
| 能力发现 | `internal/reporting/discovery.go` | string 字段 FilterOperators 加后缀族 |
| 测试 | `filter_suffix_test.go`、`filter_suffix_integration_test.go`、`reporting/discovery_test.go`、`tests/browser/analysis-model.spec.ts` | 3 个领域单测 + 真实 ClickHouse 测试 + 发现广告 + 控制台模型 |
| 控制台 | `apps/console/src/analysis-model.ts` | 算子标签（结尾是/结尾不是）与字符串 fieldOperators；saved-analysis 复用同一函数自动传播 |
| 文档 | `docs/analysis.md`、`docs/reporting-discovery.md`、`docs/feature-coverage.md` | 字符串谓词三件套与能力广告 |

## 验证

### 领域单测（`go test ./internal/analysis`、`./internal/reporting`）

- `TestFilterSuffixPlannerSQLPreservesGuard`：断言 suffix/not_suffix 的 SQL 含 `endsWith(`/`NOT endsWith(` 且仍带 `JSONHas(` 存在性守卫。
- `TestFilterSuffixValidationClosedSet`：客户端字段形状下两算子标量值通过、数组值拒绝、未知算子拒绝。
- `TestFilterSuffixResolvedTypeRules`：string 通过、number 字段拒绝。
- `TestFieldCapabilitiesAdvertiseNegationOperators` 扩展：断言 string 广告含 suffix/not_suffix，number/array 不广告。

### 真实 ClickHouse 集成（`-tags=integration -race`，独立隔离库 + 真实迁移视图）

- `TestClickHouseSuffixFiltersKeepMissingExcluded`：5 行有 `file` 值（3 个 `.csv`、1 个 `.txt`、1 个 `.pdf`）+ 1 行缺键。`suffix=.csv` 命中 3；`not_suffix=.csv` 命中 2（两个存在的非 csv），缺键行被守卫排除。**数据刻意不对称（3 vs 2）**，确保否定退化为肯定时计数不再巧合相等。
- 整个 `internal/analysis` 集成包通过，耗时 84.0s，EXIT=0，零 FAIL。

### 控制台

- Playwright 模型套件 107 项通过（扩展的算子广告测试覆盖 suffix/not_suffix 的字符串专属与标签）；`tsc --noEmit`、prettier 全过；临时 model 配置用完即删。

### 变异红绿（sha256 逐字节还原，禁用 git checkout）

后端（绿色态基线 `/tmp/evia-check/suffix/SHA256SUMS`）：

1. 把 `not_suffix` 的 `NOT endsWith` 改成 `endsWith`（退化为肯定）→ 单测 `TestFilterSuffixPlannerSQLPreservesGuard` 立即变红；真实 ClickHouse 集成同样变红（not_suffix 返回 3 而非 2）。过程中发现首版集成数据正/反计数恰好相等（各 2），退化后仍通过，属弱测试，已重设为不对称数据后变异才能可靠捕获。还原后四文件 `sha256sum -c` 全 OK、复绿。

前端（基线 `/tmp/evia-check/suffix-fe/SHA256SUMS`）：

2. 从字符串 `fieldOperators` 移除 `suffix` → 模型测试精确变红；还原后哈希 OK、107 项复绿。

## 明确未做（边界）

- 不提供通用 `not` 节点、不为数组字段加后缀算子；后缀仅字符串标量。
- 不动看板参数算子白名单、过滤深度/节点预算、值数组 20 项与字符串 1024 rune 等既有护栏。
- 不改变 prefix/contains 及其否定的任何 SQL 输出；后缀是独立新增分支。
