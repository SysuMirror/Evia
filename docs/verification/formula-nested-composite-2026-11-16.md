# 嵌套复合公式指标验证记录（2026-11-16）

## 问题（确定性缺口）

复合公式指标 v1 的表达式只能引用已发布**原子**指标版本：PG 解析器固定从 `metric_versions` 读取并硬编码 `Composite:false`，依赖见证外键也只指向原子版本表，worker 把每个直接引用都当原子叶子构建请求。纯领域 DAG 引擎却早已为嵌套预留完整能力（`FormulaResolvedMetric.Composite/References`、依赖链深度≤4、展开原子叶子≤32、环/自引用检测及对应单测），存储与执行层未接通，导致「公式引用公式」无法发布。本切片打通嵌套 DAG 的持久化、校验、见证与执行全链路。

## 范围与语义决策

1. **不改任何线网契约**：公式表达式的 `metric` 节点仍是通用 `metric_id+version` 引用，引用类型由服务端按不可变版本表归属判定，OpenAPI 与 api-client 零变更。
2. **双目标依赖见证（迁移 033）**：`formula_metric_dependencies` 保留通用身份列，新增可空 `ref_atomic_*`/`ref_formula_*` 两对外键分别指向 `metric_versions` 与 `formula_metric_versions`，soundness CHECK 要求恰有一对非空且与身份列一致；动态定位并替换旧单目标外键，历史见证全部回填为原子目标。发布时按校验图 `AtomicLeaves` 成员关系选择外键目标，数据库层继续保证引用已发布且同项目/环境。
3. **解析器双语义**：`pgFormulaResolver.ResolveFormulaMetric` 先查 `formula_metric_versions`，命中则返回复合节点（公式单位、定义摘要、直接引用清单），未命中再走原子版本；两类引用都在同事务复查 `metrics.read` 与环境一致性，禁止隐式最新版本。
4. **递归执行计划**：worker `LoadFormulaExecution` 把根版本展开为 `FormulaExecutionPlan`——复合节点映射到规范化公式，原子叶子映射到可执行 trend 请求；共享叶子只构建一次。引擎对原子叶子扇出后，由新纯函数 `EvaluateFormulaPlan` 按 DAG 自底向上逐级求值，共享子公式只算一次，并保留访问态环检测兜底。
5. **预算与语义不变**：嵌套图复用既有依赖链≤4、展开原子指标≤32、表达式树深≤8/节点≤64/引用≤8；null 传播、除零为 null、单位保守代数与对齐规则在每一级独立生效。
6. **不可变事实语义延续**：见证钉的是不可变版本，归档或改版父指标不影响已发布嵌套版本；跨环境复合引用与不存在的复合版本均 fail-closed。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 迁移 | `033_formula_dependency_composite_refs.sql` | 动态替换单目标外键、双目标外键、回填、soundness CHECK |
| 领域 | `reporting/formula_plan.go`（新） | `FormulaExecutionPlan` 与纯函数 `EvaluateFormulaPlan`（记忆化递归、环兜底） |
| 领域 | `reporting/formula_composite_engine.go` | 加载器接口改返回计划；引擎扇出原子叶子后走递归求值 |
| 存储 | `postgres/formula_metrics.go` | 解析器复合分支；发布见证按原子叶子集合选外键目标 |
| 存储 | `postgres/formula_metric_queries.go` | `LoadFormulaExecution` 递归展开复合节点与原子叶子 |
| 控制台 | `formula-metrics/formula-metrics.tsx` | 操作数选择器合并已发布原子/复合指标，过滤当前指标防直接自引用 |

## 验证

### 纯领域单测（`internal/reporting`）

- 新增 `formula_plan_test.go`：两级嵌套（C=A/B=5，root=C*A=50）求值正确、共享叶子记忆化；计划缺节点 fail-closed；损坏计划中的环 fail-closed。
- `formula_composite_engine_test.go` 适配计划接口并新增 `TestCompositeEngineEvaluatesNestedComposite`：内层引擎只收到两个去重后的原子请求，嵌套结果 50 正确；既有单层扇出、加载错误、叶子错误中止用例保持通过。
- 既有 `formula_dag_test.go` 的嵌套展开、深度/展开预算、环检测用例无需改动即继续守护图形状。

### 真实 PostgreSQL 集成（隔离 schema，`-tags=integration`）

新增 `TestFormulaMetricNestedCompositeExpansion`：发布 C=A/B（number）后发布 D=C*A（event）；D 的直接依赖恰为 C 与 A 两条，见证表中 D 恰有 1 行原子外键、1 行复合外键，C 的两条见证仍全部为原子目标；`LoadFormulaExecution` 计划含 D、C 两个复合节点与 A、B 两个原子叶子请求；D v1 可正常入队且保持 `formula_ref` 标记；production 复合引用 development 复合版本被拒；引用不存在的复合版本不会静默降级为原子查找。升级模拟 `TestMetricUpgrade...` 通过：033 依赖 013 的表，随 `Migrate()` 在 013 建表后应用，旧外键动态替换与历史见证回填成功。

### 变异红绿（三轮，sha256 逐字节还原）

基线哈希记录于 `/tmp/evia-check/nested-formula-baseline.sha256`：

1. 解析器强制走原子分支（`if e == nil` 改 `if false`）→ D 发布校验报 `METRIC_VERSION_NOT_FOUND`，集成测试变红；
2. `EvaluateFormulaPlan` 跳过复合节点 → 嵌套引擎与纯递归三个用例同时报 `INVALID_FORMULA` 变红；
3. 见证强制写原子外键列（条件改恒真）→ D 发布触发 `formula_metric_dependencies_atomic_ref_fkey` 外键违约（SQLSTATE 23503）变红。

三轮均用 Edit 精确还原并 `sha256sum -c` 三个文件全部 OK，重跑单测与 PG 集成转绿。

### 门禁

- `pnpm typecheck` 零错误、`pnpm build` 成功、prettier 通过；
- `go build ./...` 通过；`internal/reporting` 单测全绿；
- 完整 `make check` 与全量 PG 集成包结果见提交时复核；codegen 无需重跑（无契约变更），gofmt/命名检查无禁词。

## 明确未做（边界）

- 单位换算仍未交付：嵌套只复用现有保守单位代数，不做任何量纲转换；
- 看板直接引用指标版本、身份关联图、热力图/会话回放仍为独立缺口；
- 控制台无组件级单测设施，前端以 tsc 严格类型、生产构建与既有请求槽模式保证；全栈浏览器 e2e 属凭证门控，不在默认 `make check`；
- 见证只记录**直接**引用（与既有清单语义一致），完整原子展开由 DAG 校验图与 worker 计划在运行时推导，不额外物化传递依赖行。
