# 擦除作业态闭集守卫（ErasureJob.state）验证记录（2026-11-19）

## 问题（确定性缺口）

`erasure_jobs` 生命周期六态（`queued/hiding/purging/rebuilding/completed/failed`）已在 [erasure.go](../../backend/internal/erasure/erasure.go) 以 `State*` 常量维护，Go 侧全部比较与赋值（worker、postgres 存储、transport、集成测试）均已使用常量；迁移 015 的表定义也带同值 CHECK 约束。但与查询作业态切片（queryjob-state）同构的缺口仍在：**没有任何机械守卫把 Go 闭集与发布契约 `ErasureJob.state` 枚举绑定**，未来单边增删状态不会在构建期暴露。

## 方案（纯增量、行为不变）

1. [erasure.go](../../backend/internal/erasure/erasure.go) 在既有 `State*` 常量块后新增 `ErasureStates` 切片，作为 Go 侧单一真相源；不改动任何常量值与调用点；
2. 新增 [erasure_state_contract_test.go](../../backend/internal/erasure/erasure_state_contract_test.go)：`TestErasureStateVocabularyMatchesContract` 读嵌入 bundle，断言 `ErasureJob.state` enum 与 `ErasureStates` 排序后集合相等，零外部依赖；
3. raw SQL 中的状态字面量（`erasure_jobs.go` 11 处 `state='...'`/`state IN (...)`）刻意保留——它们活在查询文本内，由迁移 015 的 CHECK 约束做存储侧兜底，与查询作业态切片的边界选择一致。

## 守卫有效性取证（变异红绿）

基线 sha256（erasure.go / erasure_state_contract_test.go）存 `/tmp/evia-check/erasure/sha256-baseline.txt`，原件备份于 `/tmp/evia-check/erasure/baseline/`。

- 向 `ErasureStates` 注入 `"drift_probe"` → 守卫报 `erasure state drift: contract [completed failed hiding purging queued rebuilding] != Go [completed drift_probe failed ...]` 变红；
- 从备份还原后 `sha256sum -c` 两文件 OK，复跑 `go test ./internal/erasure/` 复绿，`grep drift_probe internal/erasure/` 零残留。

## 验证

- gofmt 零差异；`go test ./internal/erasure/ -count=1` 全包通过；
- 全量 `make check` EXIT=0、零 FAIL：Go 33 包 ok（`go test -race ./...`）、api-client 51、browser-sdk 63，命名/契约/边界/prettier/typecheck/生产构建全过。日志：`/tmp/evia-check/erasure/make-check.log`；
- 真实 PostgreSQL 回归：`go test -race -tags integration -skip TestFailoverBaselineWrite ./internal/storage/postgres` → ok 250.4s、零 FAIL；其中 erasure 专项 5 个集成测试（生命周期权威与幂等、claim 墓碑屏障与状态机、租约过期重排队与尝试上限、失败保留墓碑、worker 驱动完结）显式复跑全部 PASS（2.9s）。日志：`/tmp/evia-check/erasure/pg-integration.log`。`TestFailoverBaselineWrite` 按既有环境门控跳过。

## 明确未做（边界）

- 不改动 raw SQL 与迁移 015 的 CHECK 约束；
- 不改变任何运行时行为、契约内容或 API 形状；
- 不引入 `ValidErasureState` 助手：当前无入站状态校验调用点（状态机由存储层 transition map 与 CHECK 约束收口），避免新增无人调用的代码；
- `observer.go` 的 `JobOutcome*` 是内部观察者事件词汇、不经过 wire 契约，不在本切片范围。
