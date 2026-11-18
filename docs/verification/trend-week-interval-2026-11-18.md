# 趋势周间隔（interval=week）验证记录（2026-11-18）

## 问题（确定性缺口）

趋势查询的时间分桶闭集只有 `minute`/`hour`/`day`，缺少周粒度。在 31 天趋势窗口内，按天最多 31 个桶、缺少「按周对比」这一常用口径；能力发现 `Intervals` 也只广告这三项。本切片新增周一为起点的 `week` 间隔，不新增查询类型、不改趋势窗口预算。

## 范围与语义决策

1. **只加固定时长的 `week`，刻意不加变长的 `month`**：minute/hour/day/week 都是固定时长单位，控制台提交前的桶数估算与图表轴 `intervalMs` 都依赖「间隔→固定毫秒」映射（week=604800000）。`month` 是变长单位（28–31 天），会破坏该固定时长不变量；且 31 天趋势窗口最多产生 1–2 个月桶，价值很低。自然月队列分析已由留存查询的 `month` 粒度承担，职责不重叠。
2. **周一起算，与留存日历一致**：planner 对 week 渲染 `toStartOfWeek(event_time,1,tz)`，ClickHouse mode 1 即周一为一周起点，与留存评估器的 Monday-starting week 口径统一；时区作为绑定参数传入，桶边界按请求时区的本地日历对齐。
3. **既有三类间隔 SQL 字节不变**：minute/hour/day 仍走原 `toStartOfMinute/Hour/Day(event_time,tz)` 单参数路径，week 是独立分支，不改变任何既有计划输出。
4. **桶标签沿用既有约定**：`clickhouse.go` 把 `bucket_ms` 统一渲染为 UTC RFC3339。因此时区正确性体现在**分组**（同一事件在不同时区归入不同周桶），而标签是「请求时区下的本地周一日期」按 UTC 午夜标注，与 day/hour/minute 的渲染约定完全一致，不引入新的时间表示。
5. **闭集 fail-closed 不削弱**：未知间隔（如 `month`/`year`）仍以 `INVALID_INTERVAL` 拒绝；控制台四个共享该闭集的面同步放开，避免出现「后端支持但部分 UI 闭集陈旧」的广告不一致。
6. **全闭集同步，不遗漏任何消费面**：interval 联合类型在 analysis、metrics run、formula-metrics、saved-analysis 四处重复定义，另被图表轴 `result-chart.ts` 的 `intervalMs` 消费；本切片逐一同步，`tsc` 还实际捕获了图表轴这一容易遗漏的消费点。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 契约 | `contracts/query/query-request.schema.json`、`tooling/verify_contracts.py` | interval 枚举加 `week`；重跑 codegen（Go 离线 bundle；openapi 经外部 `$ref` 跟随，TS 联合类型两处同步）；契约校验器原本把 `week` 当作 MetricExecute 的非法 interval 负例，闭集扩展后该夹具已过时，改为仍在闭集外的 `month`，保留「闭集外 interval 必须被拒」的负例覆盖 |
| 领域校验 | `internal/analysis/validation.go` | 趋势 interval 闭集加 `week` |
| 计划器 | `internal/analysis/planner.go` | week 独立分支 `toStartOfWeek(event_time,1,tz)`；其余三类保持原单参数调用 |
| 能力发现 | `internal/reporting/discovery.go` | `Intervals` 广告加 `week` |
| 测试 | `trend_interval_test.go`、`trend_interval_integration_test.go`、`reporting/discovery_test.go`、`transport/control/metrics_test.go`、`transport/control/formula_metrics_test.go` | 2 个领域单测 + 1 个真实 ClickHouse 测试（UTC/上海双时区）+ 发现广告断言；两个 HTTP 严格输入测试原本也把 `week` 当非法 interval（期望 400），同步改为 `month`，保留闭集外拒绝覆盖 |
| 控制台 | `analysis-model.ts`、`analysis.tsx`、`metrics/model.ts`、`metrics/run.tsx`、`formula-metrics/model.ts`、`formula-metrics.tsx`、`saved-analysis-model.ts`、`saved-analysis-editor/grouping.tsx`、`saved-analysis-run/result-chart.ts` | 4 个面的联合类型/选择器/快照标签/校验闭集 + 提交前桶大小与图表轴 `intervalMs` |
| 文档 | `docs/analysis.md`、`docs/reporting-discovery.md`、`docs/feature-coverage.md` | 周间隔语义、预算表、能力广告，并核销 reporting-discovery 残留的留存陈旧广告 |

## 验证

### 领域单测（`go test ./internal/analysis`、`./internal/reporting`）

- `TestTrendWeekIntervalPlannerSQL`：断言 week 计划含 `toStartOfWeek(event_time,1,` 且保留 `bucket_ms` 投影；同时断言 minute/hour/day 仍分别是 `toStartOfMinute/Hour/Day(event_time,`，钉住既有 SQL 字节稳定。
- `TestTrendIntervalValidationClosedSet`：`week` 正向通过；`month`、`year` 仍被拒绝（既有 template 测试另覆盖未知 interval 负例）。
- `TestCapabilitiesAdvertiseWeekInterval`：断言发现接口 `Intervals` 恰好含 minute/hour/day/week。

### 真实 ClickHouse 集成（`-tags=integration -race`，独立隔离库 + 真实迁移视图）

- `TestClickHouseWeekIntervalBuckets`（PASS）：播种跨周一边界的 4 个事件（2026-11-08 周日、17:00 边界事件、11-09 周一、11-10 周二）。UTC 下两个周日事件归入周一起点 2026-11-02 的桶（计数 2），周一/周二归入 2026-11-09 桶（计数 2）；切到 Asia/Shanghai 后，17:00 边界事件已属本地周一，移入 2026-11-09 周桶（两桶变为 1/3），实测钉住「周一起算 + 时区局部分组」。
- 整个 `internal/analysis` 集成包 `go test -race -tags=integration -count=1` 通过，耗时 81.7s，EXIT=0，零 FAIL。

### 控制台

- Playwright 模型套件 107 项通过（新增 analysis 的周间隔透传/桶界测试与 saved-analysis 的周闭集正/负测试）；`tsc --noEmit` 通过（编译期实际捕获图表轴 `intervalMs` 缺 week 的遗漏）；prettier 全过。
- 临时 `playwright.model.config.ts` 用完即删，无残留。

### 变异红绿（sha256 逐字节还原，禁用 git checkout）

后端（绿色态基线 `/tmp/evia-check/week-interval/SHA256SUMS`）：

1. 把 `toStartOfWeek` 的 mode 从 1 改成 0（周日起算）→ 领域单测 `TestTrendWeekIntervalPlannerSQL` 立即变红；真实 ClickHouse 集成也变红（4 个事件全部塌缩进单一周日桶 2026-11-08，而非两个周桶），从端到端证明 mode 1 是承重语义。还原后三文件 `sha256sum -c` 全 OK、复跑复绿。

前端（基线 `/tmp/evia-check/week-interval-fe/SHA256SUMS`）：

2. 从 saved-analysis 的 interval 校验闭集移除 `week` → 新模型测试 `saved trend accepts the week interval but keeps the closed set` 精确变红；还原后哈希 OK、107 项复绿。

## 顺带核销的陈旧广告

`docs/reporting-discovery.md` 仍写留存 `retention_granularities` 为 `day`/`week`、窗口 84 天，这是上一留存月切片漏改的文档点，与实际 `discovery.go` 已广告的 `day`/`week`/`month` 与 372 天预算矛盾。本次在同一份「能力发现准确性」文档中修正为 day/week/month 与 372 天（日/周仍 84 天），纯文档事实订正，无代码行为变化。

## 明确未做（边界）

- 不引入 `month` 趋势间隔（变长、低价值、破坏固定桶宽不变量）；自然月分析仍由留存查询承担。
- 不改变趋势 31 天窗口、结果行 500 上限、桶结果的稀疏返回方式（后端不做零填充，缺失桶不伪造为 0）。
- 不改变 minute/hour/day 的任何 SQL 输出；周桶仅新增独立分支。
- 控制台无组件级单测设施，选择器交互以纯函数模型测试 + tsc + 生产构建为证据；全栈 browser-test 需凭证运行时，不在默认门禁。
