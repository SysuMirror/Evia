# 百分位指标族（p50/p90/p95/p99）验证记录（2026-11-18）

## 问题（确定性缺口）

指标数值聚合此前只有一个百分位算子 `p95`，中位数（p50）、p90、p99 等常见分布口径无法表达，调用方只能退回到外部二次计算。本切片把百分位从单一 `p95` 扩展为显式闭集族 `p50`/`p90`/`p95`/`p99`，不新增查询类型、不改指标资源模型与单位代数。

同时核销上一留存切片遗留的**能力发现广告不一致**：自然月粒度上线后，发现接口 `Capabilities` 仍只广告 `day`/`week`、窗口预算仍写 84 天，与实际放行的 `month` 和 372 天上界矛盾，会误导 UI 与调用方的能力判断。

## 范围与语义决策

1. **百分位即分位级别的薄映射，不引入新算子类别**：四个算子在 planner 内共用一张 `percentileLevels` 表（`p50→0.50`、`p90→0.90`、`p95→0.95`、`p99→0.99`）。精确精度渲染 `quantileExactIf(level)`，近似精度渲染 `quantileTDigestIf(level)`；既有 `sum/avg/min/max` 的 `*If` 路径与空观测 `if(countIf(...)=0,NULL,...)` 包装完全不变，null 语义与「无观测返回 null 而非伪造 0」一致。
2. **闭集 fail-closed 不被削弱**：领域校验 `Validate` 的算子白名单显式列出四个百分位，未知算子（如 `p42`）仍以 `INVALID_METRIC` 拒绝；近似精度只对 `unique` 与四个百分位放行，`sum` 等其他数值算子选 `approximate` 仍被拒绝。
3. **单位策略与原 p95 完全一致**：四个百分位都要求数值字段，兼容单位仍为 `number`/`millisecond`/`second`/`byte`，不新增量纲、不做单位换算；单位与聚合不兼容时拒绝保存的既有规则不变。
4. **发现接口三处对齐**：`Aggregations`、`ApproximateAggregations` 与数值字段 `FieldCapabilities.MetricOperations` 同步加入四个百分位；并把 `RetentionGranularities` 修正为 `day`/`week`/`month`、`RetentionMaxWindowSeconds` 修正为 `372*86400`（自然月 12 期 × 31 天保守上界），与 `retentionWindow` 实际预算一致。
5. **历史时点证据不回改**：`retention-analysis-2026-11-14.md` 里的「84 天硬顶」是月粒度上线前的时点记录，保留原貌；只核销仍在对外生效的活文档（feature-coverage）与活代码注释（validation.go 的 `retentionWindow` 注释从「周矩阵 84 天最大」改为「月矩阵 372 天最大」）。
6. **控制台同构**：指标编辑器与保存分析编辑器的数值算子、近似门控、结果标签（P50/P90/P95/P99）同步；近似精度选择器仅对 `unique` 与四个百分位可用，默认精确。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 契约 | `contracts/query/query-request.schema.json`、`contracts/http/openapi.json` | metric op 枚举加 `p50`/`p90`/`p99`（`p95` 既有）并补百分位语义与近似精度描述；重跑 codegen（Go 离线 bundle `contracts.gen.json`、TS `schema.gen.ts`） |
| 领域 | `internal/analysis/validation.go` | 算子闭集加三个百分位；近似精度放开给四个百分位；顺带修正 `retentionWindow` 的 84/372 天陈旧注释 |
| 领域 | `internal/analysis/planner.go` | `metric()` 内 `percentileLevels` 映射，精确 `quantileExactIf`、近似 `quantileTDigestIf` |
| 能力发现 | `internal/reporting/discovery.go` | Aggregations/ApproximateAggregations/数值字段 MetricOperations 加四项；留存粒度补 `month`、窗口预算改 372 天 |
| 测试 | `analysis/planner_test.go`、`analysis/numeric_fields_integration_test.go`、`reporting/metric_test.go`、`reporting/discovery_test.go` | 2 个领域单测 + 1 个真实 ClickHouse 测试 + 单位策略表 3 行 + 发现断言（day/week/month、372 天） |
| 控制台 | `metrics/model.ts`、`saved-analysis-model.ts`、`saved-analysis-editor/metrics.tsx`、`saved-analysis-run/result-labels.ts`、`tests/browser/metrics-model.spec.ts` | 算子/数值/近似闭集、标签与近似门控、模型测试 |
| 文档 | `docs/analysis.md`、`docs/metrics.md`、`docs/feature-coverage.md` | 百分位族口径、近似精度说明、能力覆盖与留存窗口广告核销 |

## 验证

### 领域单测（`go test -race ./internal/analysis`）

- `TestMetricPercentileQuantileLevels`：对四个算子分别断言精确 SQL 含 `quantileExactIf(0.50/0.90/0.95/0.99)`、近似 SQL 含 `quantileTDigestIf(level)`。该用例走 `Compile`，字段带服务端解析后的 `ResolvedType:"number"`。
- `TestMetricPercentileValidationClosedSet`：走 `Validate`，使用**不带** `ResolvedType` 的客户端原始字段形状（预置服务端类型本就该被 `INVALID_FIELD` 拒绝）。正向确认四算子精确/近似均通过；负向确认 `sum` + `approximate` 被拒、未知算子 `p42` 被拒，闭集防护成立。
- `reporting/metric_test.go` 单位策略表新增 p50/p90/p99 三行，断言兼容单位与 p95 同为 `number`/`millisecond`/`second`/`byte`。

### 真实 ClickHouse 集成（`-tags=integration -race`，独立隔离库 + 真实迁移视图）

- `TestClickHousePercentileLevels`（PASS）：播种 101 行 `score=1..101` 的有序值，使每个分位级别都落在整数索引、`quantileExact` 无插值歧义。精确结果逐值断言 p50=51、p90=91、p95=96、p99=100；近似 TDigest 仅断言结果为落在 `[1,101]` 的有限数（近似算法不承诺与精确值逐字节相等）。
- 整个 `internal/analysis` 集成包 `go test -race -tags=integration -count=1` 通过，耗时 76.1s，EXIT=0，零 FAIL。

### 控制台

- Playwright 模型套件 104 项通过；`metrics-model.spec.ts` 断言四个百分位的 `compatibleUnits` 与 p95 同单位、`supportsApproximate` 对四百分位为 true 而 `count` 为 false、`isNumericOp('p99')` 为 true。
- `tsc --noEmit` 通过；prettier 全过；生产构建在 make check 内通过。

### 变异红绿（sha256 逐字节还原，禁用 git checkout）

后端（基线 `/tmp/evia-check/percentile/SHA256SUMS`，覆盖 planner.go、validation.go）：

1. 把 planner 中 `p99` 的 level 从 `0.99` 改成 `0.90` → `TestMetricPercentileQuantileLevels` 精确 SQL 断言立即变红；精确字符串还原后 `sha256sum -c` OK，复跑复绿，证明分位级别映射是被测试钉住的必要逻辑。

前端（基线 `/tmp/evia-check/percentile-fe/SHA256SUMS`，覆盖 metrics/model.ts）：

2. 从 `approximateOps` 移除 `p99` → 模型测试 `supportsApproximate('p99')` 变红；还原后哈希 OK、复跑复绿，证明近似门控闭集受测试保护。

### 门禁

- `make check PYTHON=…/venv/bin/python` 一次通过（EXIT=0、零 FAIL）：命名、契约校验、Go `-race` 全量 32 包 ok、prettier、typecheck、test:client 49/49、test:sdk 63/63、生产构建全绿。
- codegen 仅产生枚举扩展的预期 diff，无手改生成文件；当前工作区 planner.go/validation.go/model.ts 与基线哈希逐一 OK。

## 明确未做（边界）

- 只开放 p50/p90/p95/p99 四个固定级别；不支持调用方自定义任意分位（如 p97.5），需要时再走显式闭集扩展，解析器不接受裸百分比参数。
- 近似 TDigest 与精确 quantileExact 不保证逐值相等，契约与测试只要求近似结果有限且在值域内；需要精确分布口径时应显式选择默认的 exact。
- 不新增单位、不做跨量纲换算；四个百分位的单位语义与原 p95 完全相同。
- 发现接口的留存广告核销只对齐既有 `month` 能力与 372 天预算，不改变留存期数闭集（1–12）、10 万行有界拉取等任何留存运行时行为。
- 控制台无组件级单测设施，选择器交互以纯函数模型测试 + tsc + 生产构建为证据；全栈 browser-test 需凭证运行时，不在默认门禁。
