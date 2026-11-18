# 分析筛选否定算子（not_in / not_prefix / not_contains）验证记录（2026-11-18）

## 问题（确定性缺口）

分析查询筛选 DSL 只有 `eq`↔`neq`、`is_present`↔`is_missing` 成对的否定，而 `in`、`prefix`、`contains` 三个算子没有对应否定：调用方无法表达「不属于某集合」「开头不是」「不包含子串」，数组字段也无法表达「不包含某元素」。逻辑节点只有 `and`/`or`，没有通用 `not`。本切片在不新增查询类型、不改写资源模型的前提下，按既有 `neq` 的模式补齐对称否定闭集。

## 范围与语义决策

1. **守卫-反转，而非整段 NOT（核心不变量）**：每个叶子谓词都编译成 `(scope AND present AND valid AND <内层比较>)`。既有 `neq` 不是 SQL `NOT(整段)`，而是保留同一 `present/valid` 守卫、只反转内层比较，因此缺失键、显式 null、类型不符的值仍然被排除。三个新算子严格沿用该模式：
   - `not_in` → `… AND value NOT IN (…)`；
   - `not_prefix` → `… AND NOT startsWith(value, x)`；
   - `not_contains`（标量字符串）→ `… AND position(value, x)=0`；
   - `not_contains`（数组）→ `… AND NOT has(value, x)`。
2. **刻意不提供通用 `not` 节点**：若用 SQL `NOT(scope AND present AND valid AND 内层)` 实现，三值逻辑会把缺失/非法行重新折叠进结果集，与 `neq` 已确立的「缺失仍排除」语义冲突。架构上唯一自洽的选择就是显式否定算子，故不存在 `not` 这一替代方案。
3. **空 `not_in` 语义**：空集合下 `in` 恒假（既有常量折叠 `0`）；`not_in` 命中所有「通过同一存在性/合法性守卫」的行，即输出 `(scope AND present AND valid)`，依旧排除缺失行，而不是退化成无条件真。
4. **类型规则与正算子对称**：`not_in` 接受数组值且适用于全部标量类型（string/boolean/number/integer/datetime）；`not_prefix`/`not_contains` 仅字符串；数组字段只新增 `not_contains`（数组本就没有 `in`，故不引入 `not_in`），空数组上 `NOT has(...)` 为真、缺失数组仍被守卫排除。
5. **fail-closed 闭集不削弱**：未知算子（如 `not_gt`）仍以 `INVALID_FILTER_OPERATOR` 拒绝；`not_in` 传标量、文本否定传数组分别按值形状规则拒绝；字符串文本算子作用于数值字段在 `CheckResolved` 阶段拒绝。
6. **范围边界（刻意保守）**：本切片只放开**分析查询筛选语言**（即席查询、保存分析、能力发现、控制台分析/保存分析编辑器）。看板参数 `parameter.go` 的每类型算子白名单（`stringOperators/numberOperators/booleanOperators`）与 OpenAPI 内联参数枚举是独立闭集，**本次不动**，作为显式边界，避免跨资源扩大影响面。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 契约 | `contracts/query/query-request.schema.json` | 比较过滤 op 枚举加 `not_in`/`not_prefix`/`not_contains`；重跑 codegen（Go 离线 bundle；openapi 经外部 `$ref` 跟随该 schema，故 `pnpm generate:api` 后 TS 联合类型同步） |
| 领域校验 | `internal/analysis/validation.go` | 算子闭集加三项；`not_in` 放行数组值、标量非数组拒绝；文本否定按标量校验 |
| 领域类型规则 | `internal/analysis/fields.go` | 数组 `not_contains` 标量规则；字符串文本否定限 string；标量 `not_in` 数组值规则 |
| 计划器 | `internal/analysis/planner.go` | 数组 `NOT has`、标量 `NOT IN`（含空集守卫语义）、`NOT startsWith`、`position()=0`，全部保留同一 guard |
| 能力发现 | `internal/reporting/discovery.go` | base 标量集加 `not_in`、string 加文本否定、数组加 `not_contains` |
| 测试 | `filter_negation_test.go`、`filter_negation_integration_test.go`、`reporting/discovery_test.go` | 3 个领域单测函数 + 1 个真实 ClickHouse 测试（8 子用例）+ 发现广告断言 |
| 控制台 | `analysis-model.ts`、`saved-analysis-model.ts`、`dashboards/runtime-filters.tsx`、`tests/browser/analysis-model.spec.ts` | 算子标签、四类 `fieldOperators`、数组值形状判定、JSON 数组输入提示与模型测试 |
| 文档 | `docs/analysis.md`、`docs/reporting-discovery.md`、`docs/feature-coverage.md` | 否定语义与守卫不变量、字段能力描述、覆盖核销与参数面边界 |

## 验证

### 领域单测（`go test ./internal/analysis`、`./internal/reporting`）

- `TestFilterNegationPlannerSQLPreservesGuard`：四种算子断言 SQL 含对应否定片段且仍带 `JSONHas(` 存在性守卫；空 `not_in` 断言保留守卫、无成员测试，空 `in` 仍折叠为 `AND 0`。
- `TestFilterNegationValidationClosedSet`：客户端原始字段形状（无 `ResolvedType`）下，三算子正向通过；`not_in` 标量、文本否定数组、未知 `not_gt` 全部拒绝。
- `TestFilterNegationResolvedTypeRules`：解析后类型规则，覆盖字符串/数值 `not_in`、数组 `not_contains` 正例与「数值字段文本否定」「数组 not_contains 传数组」「数组无 not_in」负例。
- `TestFieldCapabilitiesAdvertiseNegationOperators`：断言 string/number/array 能力广告的否定算子集合正确。

### 真实 ClickHouse 集成（`-tags=integration -race`，独立隔离库 + 真实迁移视图）

- `TestClickHouseNegationFiltersKeepMissingExcluded`（8 子用例全过）：播种 6 行（含一行属性全缺、一行有 name/score 但无 tags、一行空数组），先用三个正算子对照确认数据形状，再断言 `not_contains`=3、`not_prefix`=3、`not_in`=1、数组 `not_contains`=2（含空数组、排除缺失 tags）、空 `not_in`=5（命中全部存在 score 的行、排除缺失行），实测钉住「缺失行不被否定折叠」不变量。
- 整个 `internal/analysis` 集成包 `go test -race -tags=integration -count=1` 通过，耗时 80.6s，EXIT=0，零 FAIL；既有 `TestClickHouseDeduplicatedMetricsAndTypedFilters` 数组用例作为回归同时通过。

### 控制台

- Playwright 模型套件 105 项通过（新增 `field operators advertise negation members with labels per type`，并在类型化筛选测试补正/负用例）；`tsc --noEmit` 通过；prettier 全过。
- 临时 `playwright.model.config.ts` 用完即删，工作区无残留。

### 变异红绿（sha256 逐字节还原，禁用 git checkout）

后端（绿色态基线 `/tmp/evia-check/negation/SHA256SUMS`）：

1. 把标量 `not_contains` 的 `position(...) =0` 改成 `>0`（退化为 contains）→ `TestFilterNegationPlannerSQLPreservesGuard/not_contains_string` 精确变红；精确字符串还原后三文件 `sha256sum -c` 全 OK、复跑复绿。过程中发现首版断言片段 `)=0` 与 count 指标包装 `countIf(...)=0` 碰撞导致弱断言，已收窄为 `)=0)` 后变异才能可靠变红。

前端（基线 `/tmp/evia-check/negation-fe/SHA256SUMS`）：

2. 从字符串 `fieldOperators` 移除 `not_in` → 两个模型测试（算子广告、类型化筛选）同时变红；还原后哈希 OK、105 项复绿，证明算子闭集是被测试钉住的承重逻辑。

### 过程中发现并修复的真实缺陷

首次接入时把数组 `has(...)` 重构成自闭合变量后，仍保留了原融合写法的双闭括号 `))`，导致数组 `contains`/`not_contains` SQL 多一个右括号（ClickHouse `SYNTAX_ERROR`，Code 62），连既有正算子数组用例一并失败。真实 ClickHouse 集成立即暴露该问题，改为单闭括号后正/负数组路径全部通过——这是必须用真实依赖而非仅单测把关的直接证据。

## 明确未做（边界）

- 不提供通用 `not` 逻辑节点、不支持任意自定义否定；只开放三个与现有正算子一一对应的显式闭集成员。
- 看板参数（dashboard parameter）的每类型算子白名单与 OpenAPI 内联枚举刻意保持不变；如需在参数面开放否定算子，应作为独立切片并同步其值校验与迁移形状检查。
- 数组字段不引入 `not_in`（数组本就没有 `in`），数组否定仅 `not_contains`。
- 不改变过滤深度/节点预算（8/100）、值数组 20 项上限、字符串 1024 rune 等既有护栏，也不改变 `and/or` 结构与参数绑定方式（用户值仍走绑定参数，不进入 SQL 文本）。
