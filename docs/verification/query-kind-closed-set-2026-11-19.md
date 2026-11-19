# 查询类型闭集收敛（QueryRequest.kind 八值）验证记录（2026-11-19）

## 问题（确定性缺口）

八个查询类型（`events/trend/funnel/journey/retention/segment/path/attribution`）此前存在两份独立的 Go 副本：

- `reporting/discovery.go` 的 `discoveryQueryKinds` 切片，已由 discovery 契约守卫对齐 `QueryRequest.kind` 枚举；
- `analysis` 包内 validation/template/planner/clickhouse/raw_access 的裸字符串链与 switch，**没有任何机械对齐**——analysis 校验器漏改某值时不会被现有守卫发现。

## 方案（行为不变的纯机械收敛）

1. 新增 [query_kind_policy.go](../../../backend/internal/analysis/query_kind_policy.go)：八个 `QueryKind*` 常量、`QueryKinds` 切片与 `ValidQueryKind`，作为 Go 侧单一真相源（analysis 是领域 owner，reporting 单向依赖它，无环）；
2. analysis 包内 5 个生产文件全部改用常量/助手：两条八值长链收敛为 `!ValidQueryKind(r.Kind)`，引擎分派 switch 与分支比较改用常量；
3. reporting 的 `discoveryQueryKinds` 改为 `analysis.QueryKinds` 别名，saved_analysis/metric 策略与 control 层共 15 处改用 `analysis.QueryKind*`；
4. 测试侧 28 个文件、87 处 Go 结构体字段/赋值/比较按四种模式批量迁移；Field.Kind 词汇（metric/property/system）与 JSON 字符串夹具不受影响、原样保留。

## 常驻守卫

新增 [query_kind_contract_test.go](../../../backend/internal/analysis/query_kind_contract_test.go)：`analysis.TestQueryKindVocabularyMatchesContract` 断言 `QueryRequest.kind` 枚举与 `QueryKinds` 集合相等。既有 `reporting.TestDiscoveryVocabularyMatchesContract` 保留，由于 discovery 切片已是同一真相源的别名，两个守卫现在守护同一词汇。

## 守卫有效性取证（变异红绿）

- 基线 sha256 存 `/tmp/evia-check/query-kind/sha256-baseline.txt`；
- 向 `QueryKinds` 注入 `"drift_probe"` → analysis 域守卫与 reporting discovery 守卫**同时**报 `query kind drift ... Go 9 != contract 8` 变红，直接证明两份副本已收敛为单一真相源、任一侧漂移都会被拦截；
- 备份还原后 `sha256sum -c` 全部 OK，复跑复绿，`grep drift_probe/DriftProbe` 零残留。

## 验证

- `go build ./...`、`go vet ./...`（含全部测试编译）通过；
- 全量 `make check` EXIT=0、零 FAIL（Go 33 包、client 51、sdk 63、console 18、生产构建），日志：`/tmp/evia-check/query-kind/make-check.log`；
- 真实 ClickHouse 集成（`-race -tags integration ./internal/analysis`）84.002s 通过，覆盖八类查询在真实存储上的引擎分派；
- 真实 PostgreSQL 集成（`-tags integration -skip TestFailoverBaselineWrite ./internal/storage/postgres`）103.728s 一次通过，覆盖保存分析与导出链路上的查询类型持久化；`TestFailoverBaselineWrite` 仍是与本切片无关的既有环境门控。

## 明确未做（边界）

- 不改动 JSON 夹具、Field.Kind 词汇、生成代码与任何 SQL；
- 不改变任何运行时行为、契约内容或 API 形状；
- 各查询类型的引擎实现与护栏规则不在本切片范围，闭集只固化类型词汇本身。
