# 聚合算子闭集收敛（metrics.items.op 十值）验证记录（2026-11-19）

## 问题（确定性缺口）

十个聚合算子（`count/unique/sum/avg/min/max/p50/p90/p95/p99`）此前以裸字符串分散在 analysis 校验器、planner 的 SQL 函数/分位 map、字段校验、trend_facts 与 reporting 指标策略中；reporting discovery 的 `discoveryAggregations` 与 `discoveryApproximateAggregations` 是独立切片，只由 discovery 守卫对齐契约，analysis 侧漂移无测试可拦截——与 query kind、interval 收敛前同构的双副本问题。

## 方案（行为不变的纯机械收敛）

1. 新增 [aggregation_policy.go](../../../backend/internal/analysis/aggregation_policy.go)：十个 `Op*` 常量、`Aggregations`/`ValidAggregation`，以及近似算子子集 `ApproximateAggregations`/`ValidApproximateAggregation`（unique + 四个百分位），把「仅这些算子允许 approximate」的规则命名化；
2. 生产侧 14 处真实算子站点全部改用常量/助手：planner 的 SQL 函数 map 与分位 level map 键改常量，两条长校验链收敛为助手调用；
3. reporting discovery 两切片改为 `analysis.Aggregations`/`analysis.ApproximateAggregations` 别名，metric_policy 单位规则 switch 改用常量；
4. 测试侧 40 个文件、82 处 `Metric.Op` 赋值/比较迁移；
5. 刻意保留：`Filter.Op` 词汇（and/gte/lte/formula 等）、JSON tag/日志键中的 count/unique、schema 夹具字段名、原始 JSON 字符串——它们不是聚合算子词汇。

## 常驻守卫

新增 [aggregation_contract_test.go](../../../backend/internal/analysis/aggregation_contract_test.go)：`analysis.TestAggregationVocabularyMatchesContract` 读嵌入 bundle，断言嵌套路径 `QueryRequest.metrics.items.op` 枚举与 `Aggregations` 集合相等。既有 discovery 守卫保留，经别名与本守卫共享同一真相源。

## 守卫有效性取证（变异红绿）

- 基线 sha256 存 `/tmp/evia-check/aggregation/sha256-baseline.txt`；
- 向 `Aggregations` 注入 `"drift_probe"` → analysis 域守卫与 reporting discovery 守卫**同时**报 `aggregation drift ... Go 11 != contract 10` 变红，证明双副本已收敛；
- 备份还原后 `sha256sum -c` 全部 OK，复跑复绿，`grep drift_probe/DriftProbe` 零残留。

## 验证

- `go build ./...`、`go vet ./...`（含全部测试编译）通过；
- 全量 `make check` EXIT=0、零 FAIL（Go 33 包、client 51、sdk 63、console 18、生产构建），日志：`/tmp/evia-check/aggregation/make-check.log`；
- 真实 ClickHouse 集成（`-race -tags integration ./internal/analysis`）84.462s 通过，覆盖十算子在真实存储上的精确/近似执行；
- 真实 PostgreSQL 集成（`-tags integration -skip TestFailoverBaselineWrite ./internal/storage/postgres`）104.764s 一次通过，覆盖指标定义与执行链路上的算子持久化；`TestFailoverBaselineWrite` 仍是与本切片无关的既有环境门控。

## 明确未做（边界）

- 不改动 Filter.Op 词汇、JSON tag、schema 夹具与生成代码；
- 不改变任何运行时行为、契约内容、API 形状或 SQL 文本（map 键常量化不改变生成的查询字符串）；
- 近似算子的具体 ClickHouse 函数选择仍留在 planner，闭集只固化词汇与子集规则。
