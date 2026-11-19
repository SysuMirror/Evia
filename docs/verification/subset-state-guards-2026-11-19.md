# 子集态契约守卫（coverage.state / application_state）验证记录（2026-11-19）

## 问题（确定性缺口）

两个状态词汇的契约枚举宽于 Go 当前产出，裸字符串分散且无机械对齐；与 discovery 主体词汇（`resolved_person` 预留）同属"Go 产出子集 vs 契约完整闭集"形态：

1. `QueryResult.metadata.coverage.state` 枚举 3 值（`provisional/settled/partial`），管线当前只认证 `provisional`（[clickhouse.go](../../backend/internal/analysis/clickhouse.go) 与 [formula_align.go](../../backend/internal/reporting/formula_align.go) 两处产出）；
2. `application_state` 枚举 3 值（`pending/applied/failed`），由 Source/SourceIssued/Retention 三个 schema 共享。新来源写 `pending`，`applied` 由存储读模型的 SQL CASE 依据 `observed_at` 派生，`failed` 无任何 Go/SQL 产出方。

## 语义决策（显式记录，可逆）

采用**子集守卫**而非全闭集常量，理由：

- 如实反映当前生产——Go 侧只命名实际产出/可读的值，不引入无人引用的死常量；
- 漂移捕获力不降低：Go 产出契约外的值会变红，契约收窄删掉已产出值也会变红；契约**加项**不变红是刻意行为，预留值本就允许先行发布；
- 与既有 `TestDiscoveryVocabularyMatchesContract` 的 subject 子集断言（#3 守卫）同构，未来落地 settled/partial 或 failed 时，把新值加入 Go 切片并补产出代码即可，守卫同步升级为集合相等也只需改断言。

## 方案（行为不变的纯机械收敛）

### coverage.state（analysis 域）

- 新增 [coverage_policy.go](../../backend/internal/analysis/coverage_policy.go)：`CoverageStateProvisional` 常量与 `CoverageStates` 单值子集切片；
- 新增 [coverage_state_contract_test.go](../../backend/internal/analysis/coverage_state_contract_test.go)：`TestCoverageStateVocabularyContract` 沿 `QueryResult.metadata.coverage.state` 取 enum，断言 Go 子集每个值都在契约集合内；
- 迁移 clickhouse.go、reporting/formula_align.go 两处生产赋值，及 analysis/control/postgres/reporting 四个测试文件的 Go 结构体字面量。

### application_state（ingestion 域）

- 新增 [application_state_policy.go](../../backend/internal/ingestion/application_state_policy.go)：`ApplicationStatePending/Applied` 常量与 `ApplicationStates` 两值子集切片；
- 新增 [application_state_contract_test.go](../../backend/internal/ingestion/application_state_contract_test.go)：`TestApplicationStateVocabularyContract` 遍历 Source/SourceIssued/Retention 三个 schema 的同一 enum 做子集断言；
- 迁移 sources.go 生产赋值与 control/postgres 测试字面量。
- 刻意保留：postgres/sources.go 中派生 `applied/pending` 的 SQL CASE 字符串——application_state 是读时派生列、ingestion_sources 无存储列也无 CHECK，字符串活在查询文本内，与既有 raw SQL 边界选择一致。

## 守卫有效性取证（变异红绿）

基线 sha256 分存 `/tmp/evia-check/coverage/sha256-baseline.txt` 与 `/tmp/evia-check/app-state/sha256-baseline.txt`，原件备份于各自 `baseline/`。

- 向 `CoverageStates` 注入 `"drift_probe"` → 报 `coverage state drift: Go value "drift_probe" absent from contract [provisional settled partial]` 变红；
- 向 `ApplicationStates` 注入 `"drift_probe"` → 首个 schema 即报 `application state drift on Source: Go value "drift_probe" absent from contract [pending applied failed]` 变红；
- 双双备份还原，`sha256sum -c` 全部 OK，复跑复绿，`grep drift_probe internal/ingestion internal/analysis` 零残留。

## 验证

- gofmt 零差异；`go build ./...` 通过；两域守卫与相关包单测全绿；
- 全量 `make check` EXIT=0、零 FAIL：Go 33 包 ok（`go test -race ./...`）、api-client 51、browser-sdk 63，命名/契约/边界/prettier/typecheck/生产构建全过。日志：`/tmp/evia-check/subset/make-check.log`；
- 真实 PostgreSQL 回归：`go test -race -tags integration -skip TestFailoverBaselineWrite ./internal/storage/postgres` → ok 246.6s、零 FAIL；其中 `TestSourceApplicationStateAndCrossInstanceRevocationBound`（2.50s）与全部 TestQuery* 专项显式 PASS。日志：`/tmp/evia-check/subset/pg-integration.log`。`TestFailoverBaselineWrite` 按既有环境门控跳过。

## 明确未做（边界）

- 不把 settled/partial/failed 提前命名为 Go 常量——无产出方的值进入代码即死代码；
- 不改动 SQL CASE、契约、wire 生成代码与任何运行时行为；
- Retention schema 的 application_state 无 Go 构造方（复合响应形状），守卫仍将其纳入同一 enum 校验防止契约单边漂移。
