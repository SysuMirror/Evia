# 查询预算单一真相源与广告/执行对齐守卫验证记录（2026-11-19）

## 问题（确定性缺口）

16 个查询预算长期存在**两份独立 Go 副本**：`reporting.DiscoveryBudgets` 以 16 个位置参数构造并广告给客户端，`analysis.ValidateConfiguration` 与查询准入路径以另一批裸字面量执行同一限额（如 discovery 广告 `ResultRows=500`，validation 独立写 `r.Limit > 500`；时间窗 `31*24h`/`7*24h`、队列上限 32 同理）。单边修改不会被任何测试发现，客户端看到的预算与 API 实际拒绝行为可能静默脱钩。守卫总录此前也明确记录「数值预算未机械比对」的边界。

## 方案（纯常量提取，行为字节不变）

1. 新增 [budgets.go](../../backend/internal/analysis/budgets.go)：16 个 `Budget*` 常量作为唯一真相源，`BudgetRetentionMaxWindowSeconds` 由 `BudgetRetentionMaxPeriods * 31 天` 派生，与 `retentionWindow` 的保守月上界同一公式；
2. [validation.go](../../backend/internal/analysis/validation.go) 10 处硬编码接线到常量（时间窗 2、结果行、事件选择/分组/指标、漏斗步数与窗口、留存期数、过滤深度/节点/参数/值）；[query.go](../../backend/internal/storage/postgres/query.go) 队列满判定 `queued >= 32` 接线 `BudgetProjectQueuedQueries`；
3. [discovery.go](../../backend/internal/reporting/discovery.go) 改为**命名字段构造**并全部引用 analysis 常量（同时消除位置参数错位风险）；
4. 新增 [budget_contract_test.go](../../backend/internal/reporting/budget_contract_test.go) 双层守卫；
5. retention_test 补最大窗口不变量测试。

## 刻意保留（边界）

- `query_worker.go` 中运行并发 `<4` 的 raw SQL 字面量：活在查询文本内，沿用本仓 raw SQL 边界；广告值仍由 `BudgetProjectRunningQueries` 单一来源；
- 同值但非广告预算的内部限额不耦合：路径深度/归因触点/SameEntity 的 `8`、归因窗口 `604800`、请求体 256KiB、时区长度 64——契约未广告这些字段，强行共用常量会造成策略误耦合；
- 契约 `AnalysisCapabilities.budgets` 只声明 JSON 安全整数通用界（0 到 2^53-1），产品限额值只存在于 Go 侧，这是契约形状，本切片不改变它。

## 常驻守卫（双层）

`reporting.TestDiscoveryBudgetsMatchEnforcementAndContract`：

1. **广告==执行层**：实际 Capabilities 输出的 16 个预算逐字段等于 analysis 常量结构体，防止 discovery 重新去耦合；
2. **契约数值界**：每个广告预算落在嵌入 bundle `AnalysisCapabilities.budgets.*` 的 minimum/maximum 内，并断言 Go 预算数与契约属性数相等（16），防 schema 重构缩水；
3. `analysis.TestRetentionMaxWindowMatchesAdvertisedBudget`：最大留存配置（12 期×31 天月周期）的 `retentionWindow` 秒数恒等于广告的 `RetentionMaxWindowSeconds`。

## 守卫有效性取证（变异红绿）

基线 sha256（budgets.go/discovery.go/budget_contract_test.go/retention_test.go）存 `/tmp/evia-check/budgets/sha256-baseline.txt`，原件备份于 `baseline/`。

- 变异 A（广告构造漂移）：discovery 的 `ResultRows` 改字面量 499 → 守卫报 `discovery budget drift ... ResultRows:499 ... != enforcement constants ... ResultRows:500` 变红；
- 变异 B（超契约安全整数界）：`BudgetResultRows` 改为 2^53 → 守卫报 `budget result_rows=9007199254740992 outside contract bounds [0,9.007199254740991e+15]` 变红；
- 变异 C（窗口公式漂移）：广告窗口月天数 31 改 30 → 不变量测试报 `retention max window 32140800 != advertised budget 31104000` 变红；
- 三次变异均备份还原，`sha256sum -c` 四文件 OK，复跑复绿，零残留。

## 验证

- gofmt 零差异；`go build ./...` 通过；analysis/reporting 相关单测全绿；
- 全量 `make check` EXIT=0、零 FAIL：Go 33 包 ok（`go test -race ./...`）、api-client 51、browser-sdk 63，命名/契约/边界/prettier/typecheck/生产构建全过。日志：`/tmp/evia-check/budgets/make-check.log`；
- 真实 PostgreSQL 回归：专项 `TestQueryAdmissionAndConcurrentExecutionBounds`（队列 32 上限准入）、`TestQueryJobsAuthorizationLeasesAndCancellation`、`TestReportingDryValidationTypedScalarsDependenciesAndNoWrites` 显式 PASS；整包 `go test -race -tags integration -skip TestFailoverBaselineWrite ./internal/storage/postgres` → ok 247.8s、零 FAIL。日志：`/tmp/evia-check/budgets/pg-integration.log`。

## 明确未做（边界）

- 不改变任何限额数值与运行时行为（纯标识符替换，三处窗口数学经不变量测试钉死）；
- 不把内部未广告限额纳入广告预算结构体；
- 不修改契约 numeric bounds 的通用安全整数形状。
