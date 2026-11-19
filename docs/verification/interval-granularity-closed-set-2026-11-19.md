# 查询间隔与留存粒度闭集收敛验证记录（2026-11-19）

本记录覆盖两个共享 day/week 同形词的词汇：`QueryRequest.interval`（4 值）与 `QueryRequest.retention.granularity`（3 值）。

## 问题（确定性缺口）

- 间隔词汇 `minute/hour/day/week` 在 analysis 校验器、planner 分桶 map、reporting 三个指标服务与 postgres 指标查询中以裸字符串维护，且指标执行侧只允许前三值（week 不支持）这条子集规则以重复长链出现四次；
- 留存粒度 `day/week/month` 在 analysis 校验器与 retention 评估器的多个 switch/比较中内联；
- reporting discovery 的 `discoveryIntervals`/`discoveryRetentionGranularities` 是两份独立切片，仅由 discovery 守卫对齐契约，analysis 侧的同词汇漂移不会被任何测试发现——与 query kind 收敛前同构的双副本问题。

## 方案（行为不变的纯机械收敛）

1. 新增 [interval_policy.go](../../../backend/internal/analysis/interval_policy.go)：`IntervalMinute/Hour/Day/Week` 常量、`Intervals`/`ValidInterval`，以及显式子集 `MetricIntervals`/`ValidMetricInterval`（week 排除规则命名化，不再复制字面量列表）；
2. 新增 [retention_policy.go](../../../backend/internal/analysis/retention_policy.go)：`RetentionGranularityDay/Week/Month` 常量、`RetentionGranularities`/`ValidRetentionGranularity`；
3. 生产侧 17 处全部改用常量/助手，reporting discovery 两切片改为 `analysis.Intervals`/`analysis.RetentionGranularities` 别名；
4. 测试侧 17 个文件迁移 Go 字段/比较/表驱动首列与 discovery want 集合；
5. 刻意保留：原始 JSON 夹具（wire 独立见证）、负例见证值（retention 的非法粒度 `hour`、interval 的非法值 `month`/`year`——它们不是任何闭集成员，保留裸字符串才能表达「故意非法」）、ComparisonSpec 的 day/week/month（独立比较单位词汇，已有自有常量）。

## 常驻守卫（2 个新测试）

- `analysis.TestIntervalVocabularyMatchesContract`：`QueryRequest.interval` 枚举与 `Intervals` 集合相等；
- `analysis.TestRetentionGranularityVocabularyMatchesContract`：嵌套 `QueryRequest.retention.granularity` 枚举与 `RetentionGranularities` 集合相等。

## 守卫有效性取证（变异红绿）

- 基线 sha256 存 `/tmp/evia-check/interval-gran/sha256-baseline.txt`；
- 同时向 `Intervals` 与 `RetentionGranularities` 注入 `"drift_probe"` → 两个新域守卫与 reporting discovery 守卫**同时**报 drift 变红（interval 分支、granularity 分支、discovery 别名三处），证明双副本已收敛为单一真相源；
- 备份还原后 `sha256sum -c` 全部 OK，复跑复绿，`grep drift_probe/DriftProbe` 零残留。

## 验证

- `go build ./...`、`go vet ./...`（含全部测试编译）通过；
- 全量 `make check` EXIT=0、零 FAIL（Go 33 包、client 51、sdk 63、console 18、生产构建），日志：`/tmp/evia-check/interval-gran/make-check.log`；
- 真实 ClickHouse 集成（`-race -tags integration ./internal/analysis`）84.259s 通过，覆盖四种间隔分桶与三种留存粒度的真实存储执行；
- 真实 PostgreSQL 集成（`-tags integration -skip TestFailoverBaselineWrite ./internal/storage/postgres`）104.024s 一次通过，覆盖指标执行间隔校验与留存模板持久化；`TestFailoverBaselineWrite` 仍是与本切片无关的既有环境门控。

## 明确未做（边界）

- 不改动 JSON 夹具、负例见证、ComparisonSpec 词汇与生成代码；
- 不改变任何运行时行为、契约内容、API 形状或数据库约束；
- 指标执行不支持 week 的规则仍由 `MetricIntervals` 子集表达，未来放开时只改这一处。
