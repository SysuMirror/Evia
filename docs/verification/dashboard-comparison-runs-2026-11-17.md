# 看板比较期执行扇出验证记录（2026-11-17）

## 问题（确定性缺口）

长期方案要求看板支持比较期：使用相同主体、粒度、过滤和单位，对「同一口径的上一周期」并排呈现，且明确偏移方法——「夏令时不能仅减固定秒数得到上周同日」。此前 `DashboardExecute` 只有单一时间窗，每个组件仅扇出一个查询作业，`dashboard_run_widgets` 也按组件单行存储，无法承载第二个窗口。本切片交付比较期的后端执行、关系存储、契约与读回能力（控制台双窗呈现留下一切片）。

## 范围与语义决策

1. **请求形态**：`DashboardExecute.comparison` 为可选 `ComparisonSpec{unit,amount}`；省略时行为与历史完全一致（仅 current 单行）。`unit` 闭集为 `previous_period/day/week/month/year`，日历单位 `amount` 限 1–12。
2. **DST 安全偏移**：复用 `reporting.ShiftTimeRange`（上一切片原语），日历单位在运行时区按墙钟字段重建；`previous_period` 用精确窗口时长反向平移。比较窗口在入队事务内一次性推导。
3. **双窗扇出**：每个 widget 产生 current + 可选 comparison 两个 `analysis_query_jobs`，同过滤、同粒度、同单位，仅时间范围不同；仍在单事务内原子入队，沿用项目 32 队列/4 运行预算（比较期使单 run 最多 24 个子作业，是既有预算的正确背压，不放宽）。
4. **关系模型（迁移 036）**：`dashboard_run_widgets` 增加 `period CHECK IN ('current','comparison')`，主键扩为 `(project_id,run_id,widget_id,period)`；position 唯一约束改为仅 current 行的部分唯一索引，比较期行共享 position。历史行默认 `current`，旧约束/读取路径保持兼容。
5. **状态与取消**：`DashboardRunState` 把比较期作业计为独立工作单元——任一比较期失败使 run 进入 partial/failed；取消 run 同时取消两类作业；过期判定覆盖比较期作业。
6. **幂等**：比较期规格参与输入哈希，同键同输入重放原 run（含比较期作业），改规格即 `IDEMPOTENCY_CONFLICT`。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 迁移 | `036_dashboard_comparison_runs.sql` | period 列、复合主键、current 部分唯一索引、查询索引重建 |
| 领域 | `dashboard.go`、`dashboard_policy.go`、`comparison.go` | 执行/运行/组件运行结构加 comparison；规范化闭集校验；状态聚合双作业 |
| 存储 | `dashboard_runs.go` | 比较窗口推导、双请求构建 `dashboardComparisonRequestTx`、双作业入队、period 双行写入；读路径三遍处理（解析作业→current 装配→comparison 配对）；取消双作业 |
| 契约 | `openapi.json` | `ComparisonSpec` schema；Execute/Run/WidgetRun 可选 comparison；codegen 重生（176 schema） |
| 测试 | `comparison_test.go`、`dashboard_comparison_integration_test.go` | 领域单测与 4 个真实 PG 集成测试 |

## 验证

### 真实 PostgreSQL 集成（`-tags=integration`）

- `TestDashboardComparisonFanoutAndShiftedWindow`：单 widget 比较期 run 返回两个不同作业；关系表每 period 各一行；比较期子查询的 `time_range.from` 瞬时值严格等于 `ShiftTimeRange` 推导值，当前窗口保持不变；同键重放返回同 run；改规格冲突再换新键生成新 run；取消后两个作业均为 canceled。
- `TestDashboardComparisonRejectsInvalidSpec`：未知 unit、amount=0、amount=13 全部在入队前拒绝。
- `TestDashboardWithoutComparisonKeepsSinglePeriod`：无比较期时不回显 comparison、仅写 current 行（向后兼容）。
- `TestDashboardMetricWidgetComparisonFanout`：混合 analysis+metric 双 widget 比较期产生 4 个 run_widget 行、每 widget 两个不同作业。

### 领域单测

`TestNormalizeExecutionRejectsBadComparison`（闭集/范围）、`TestDashboardRunStateCountsComparisonJobs`（比较期失败→partial、全成功→succeeded、全排队→queued），与既有 `TestDashboardRunStatesKeepMixedOutcomes` 并存通过。

### 变异红绿（两轮，sha256 逐字节还原）

基线哈希记录于 `/tmp/evia-check/comparison-fanout-baseline.sha256`：

1. 短路比较窗口推导（comparisonRange 恒 nil）→ 集成测试报「expected one widget with a comparison job」变红；
2. 状态聚合忽略比较期作业 → 单测报「failed comparison must make the run partial, got succeeded」变红。

两轮均 Edit 精确还原，`sha256sum -c` 全部 OK，复跑转绿。

### 门禁

完整 `make check` 退出码 0：32 个 Go 包 ok、0 FAIL；Node 18/41/57、gofmt/prettier/命名/契约/codegen 幂等全绿。全量 PG 集成包 `-count=1 -skip TestFailoverBaselineWrite` 新鲜通过（88.37s，0 FAIL）。`TestFailoverBaselineWrite` 属既有多主机环境门控，与本切片无关。

## 明确未做（边界）

- 控制台尚未提供比较期开关与双窗结果对比渲染（下一切片）；
- 比较期复用同一时间粒度与过滤，不支持比较期独立参数或独立粒度；
- 通用参数绑定、跨请求结果缓存仍为独立缺口。
