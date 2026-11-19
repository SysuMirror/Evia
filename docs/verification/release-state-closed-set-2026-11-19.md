# 发布双态子集守卫（Release.state / acceleration_state）验证记录（2026-11-19）

## 问题（确定性缺口）

`Release` 携带两个状态字段，Go 侧以裸字符串产出/比较，且契约枚举宽于当前实现：

- `state` 契约 5 值（draft/validating/publishing/published/failed），Go 与迁移 002 的表 CHECK 今天都只允许 `published`——其余 4 值是未实现的发布工作流预留；
- `acceleration_state` 契约 4 值（not_requested/building/ready/failed），Go 创建时写 `not_requested`，外部投递子系统经 SQL 把 delivery 记录推进到 `building`（Go 读模型经 join 可观测），`ready/failed` 无任何 Go 观测点。

两个字段均无机械对齐守卫，单边漂移无法在构建期暴露。

## 语义决策（显式记录，可逆）

沿用本批子集守卫范式（同 [subset-state-guards](subset-state-guards-2026-11-19.md)）：Go 只命名**实际产出或可经读模型观测**的值，对契约 enum 做子集断言。不提前命名 draft/validating/publishing/failed 与 ready/failed——无产出方的常量即死代码；未来发布工作流落地时把新值加入切片并补产出代码，即可平滑升级为集合相等断言。

## 方案（行为不变的纯机械收敛）

1. 新增 [release_state_policy.go](../../backend/internal/catalog/release_state_policy.go)：
   - `ReleaseStatePublished` + `ReleaseStates`（单值子集）；
   - `ReleaseAccelerationNotRequested/Building` + `ReleaseAccelerationStates`（两值可观测子集）；
2. 新增 [release_state_contract_test.go](../../backend/internal/catalog/release_state_contract_test.go)：`TestReleaseStateVocabularyContract` 对 `Release.state` 与 `Release.acceleration_state` 两个 enum 分别做子集断言；
3. 迁移 postgres/control 共 4 个文件的 Go 赋值与比较到 `catalog.*` 常量。
4. 刻意保留：
   - `releaseManifest.Status: "published"` 与迁移测试中的 manifest JSON 属 **EventPlan.status** 词汇（独立契约字段），不在本切片；
   - delivery 推进的 raw SQL 字符串（迁移 003 CHECK 兜底）；
   - `CreateRelease(..., "failed", ...)` 第四参数是**幂等键**测试值，与状态无关；
   - 表驱动测试的 `name: "published"` 用例标签。

## 守卫有效性取证（变异红绿）

基线 sha256 存 `/tmp/evia-check/release-state/sha256-baseline.txt`，原件备份于 `baseline/`。

- 变异 A：向 `ReleaseStates` 注入 `"drift_probe"` → 报 `release state drift: Go value "drift_probe" absent from contract [draft validating publishing published failed]` 变红；
- 变异 B：向 `ReleaseAccelerationStates` 注入 `"drift_probe"` → 报 `release acceleration_state drift: Go value "drift_probe" absent from contract [not_requested building ready failed]` 变红；
- 两条断言分支独立证有效；备份还原后 `sha256sum -c` OK，复跑复绿，`grep drift_probe internal/catalog` 零残留。

## 验证

- gofmt 零差异；`go build ./...` 通过；catalog 守卫与相关包单测全绿；
- 全量 `make check` EXIT=0、零 FAIL：Go 33 包 ok（`go test -race ./...`）、api-client 51、browser-sdk 63，命名/契约/边界/prettier/typecheck/生产构建全过。日志：`/tmp/evia-check/release-state/make-check.log`；
- 真实 PostgreSQL 回归：catalog 专项 6 测显式 PASS（含 `TestCatalogUpgradeFromPopulated002` 对回填 `not_requested` 的断言、`TestCatalogReleaseMembershipAndDeliverySeparation`）；整包 `go test -race -tags integration -skip TestFailoverBaselineWrite ./internal/storage/postgres` → ok 248.4s、零 FAIL。日志：`/tmp/evia-check/release-state/pg-integration.log`。

## 明确未做（边界）

- 不实现预留的发布工作流状态机，不改变任何运行时行为、契约或 API 形状；
- 不命名无产出方的预留值；外部投递子系统的 ready/failed 落地后再扩切片；
- EventPlan.status、幂等键、SQL 文本与测试标签保持原样。
