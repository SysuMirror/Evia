# 漏斗模式闭集收敛（funnel.mode）验证记录（2026-11-19）

## 问题（确定性缺口）

漏斗模式当前是单值词汇 `ordered_with_interleaving`，但存在两份 Go 副本：analysis 校验器中的裸字符串比较，与 reporting discovery 的 `discoveryFunnelModes` 切片。单值闭集同样需要命名常量——未来新增模式时，校验器漏改不会被 discovery 守卫发现。

## 方案（行为不变的纯机械收敛）

1. 新增 [funnel_policy.go](../../../backend/internal/analysis/funnel_policy.go)：`FunnelModeOrderedWithInterleaving` 常量、`FunnelModes` 切片与 `ValidFunnelMode`，作为 Go 侧单一真相源；
2. `validation.go` 的模式比较改为 `!ValidFunnelMode(f.Mode)`，reporting discovery 切片改为 `analysis.FunnelModes` 别名；
3. 测试侧 5 个文件 6 处 Go 结构体赋值迁移到常量；event_versions 测试中的原始 JSON 字符串夹具保留，作为 wire 解码的独立见证。

## 常驻守卫

新增 [funnel_contract_test.go](../../../backend/internal/analysis/funnel_contract_test.go)：`analysis.TestFunnelModeVocabularyMatchesContract` 读嵌入 bundle，沿嵌套路径 `QueryRequest.funnel.properties.mode` 取值。该属性在契约中是 **JSON Schema `const` 单值而非 `enum` 数组**（`{"const":"ordered_with_interleaving"}`），故守卫断言 `len(FunnelModes) == 1 && FunnelModes[0] == const`，与 reporting 包 `TestDiscoveryVocabularyMatchesContract` 既有的 const 分支同构；reporting 侧经 `analysis.FunnelModes` 别名共享同一真相源。

另在 [template_test.go](../../../backend/internal/analysis/template_test.go) 的漏斗负例区补一条常驻拒绝用例：`Mode: "ordered"`（语义近似的裸字符串，与 identity 集成测试既有见证同值）经 `ValidateTemplate → ValidateConfiguration` 必须返回 `INVALID_FUNNEL`，使校验器闭集判断具备可变异的行为级覆盖。

## 守卫有效性取证（变异红绿）

基线 sha256（funnel_policy.go / funnel_contract_test.go / validation.go / discovery.go）存 `/tmp/evia-check/funnel/sha256-baseline.txt`，原件备份于 `/tmp/evia-check/funnel/baseline/`。

- 变异 A（闭集单边扩项）：向 `FunnelModes` 注入 `"drift_probe"` → analysis 域守卫报 `funnel mode drift: contract const "ordered_with_interleaving" != Go [ordered_with_interleaving drift_probe]` 变红，reporting discovery 守卫在 `contract_alignment_test.go:105` 以同一消息同时变红，证明别名收敛有效；
- 变异 B（校验守卫失效）：把 `validation.go` 的 `!ValidFunnelMode(f.Mode)` 短路为 `false` → 新增负例用例报 `template_test.go:163: wanted INVALID_FUNNEL, got <nil>` 变红，证明拒绝路径真实由闭集守卫兜底；
- 两次变异均从备份还原，`sha256sum -c` 四个文件全部 OK，复跑复绿，`grep drift_probe internal/` 零残留。

## 验证

- `go build ./...`、`go vet ./...`（含全部测试编译）通过；gofmt 零差异；
- 全量 `make check` EXIT=0、零 FAIL：Go 33 包 ok（`go test -race ./...`）、api-client 51、browser-sdk 63，命名/契约/边界/prettier/typecheck/生产构建全过。日志：`/tmp/evia-check/funnel/make-check.log`；
- 真实依赖回归（容器均健康）：
  - ClickHouse：`go test -race -tags integration ./internal/analysis` → ok 84.5s、零 FAIL，含 `TestClickHouseJourneyAndOrderedFunnel`（迁移后常量赋值的漏斗全链路）。日志：`/tmp/evia-check/funnel/ch-integration.log`；
  - PostgreSQL：`go test -tags integration -skip TestFailoverBaselineWrite ./internal/storage/postgres` → ok 250.9s、零 FAIL，含 reporting discovery 集成（`discoveryFunnelModes` 别名路径）。`TestFailoverBaselineWrite` 按既有环境门控跳过（需独立多主机 DSN）。日志：`/tmp/evia-check/funnel/pg-integration.log`。
- 操作备注：首轮 CH 回归因仅 `source` env 文件（变量无 `export`）导致 `EVIA_TEST_CLICKHOUSE_URL is required` 全量 0.00s 失败，属 shell 导出问题而非断言失败；改用 `set -a && source ... && set +a` 自动导出后复跑通过。

## 明确未做（边界）

- 不改动 JSON 夹具与生成代码；
- 不改变任何运行时行为、契约内容或 API 形状；
- 新漏斗模式的引入仍属语义决策，本切片只固化既有单值的防漂移机制；const 升级为 enum 时，两侧守卫需同步改为集合断言。
