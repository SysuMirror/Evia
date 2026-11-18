# 查询作业态闭集提取（QueryJob.state）验证记录（2026-11-19）

## 问题（确定性缺口）

分析查询作业生命周期六态（`queued/running/succeeded/failed/canceled/expired`）此前以 Go 字符串字面量分散在 analysis/reporting/postgres/control/cmd 五个包的 20 余个文件中：

- 唯一的机械对齐是迁移 005 的数据库 `CHECK(state IN (...))` 约束与 OpenAPI `QueryJob.state` 枚举，Go 侧没有单一真相源，也没有契约守卫；
- `reporting/dashboard_policy.go` 内已存在一组包内 `queryJobState*` 常量（仅 5 个、缺 `failed`），其余调用点全部内联，同一词汇存在第二套副本。

## 方案（行为不变的纯机械重构）

1. 领域归属：`analysis.Job` 是查询作业的领域类型，新增 [job_state.go](../../../backend/internal/analysis/job_state.go)，定义 `JobStateQueued/Running/Succeeded/Failed/Canceled/Expired` 六常量与 `JobStates` 切片作为 Go 侧单一真相源；`analysis` 不依赖任何内部业务包，无环。
2. 删除 `dashboard_policy.go` 的包内 `queryJobState*` 副本，聚合逻辑改用 `analysis.JobState*`；看板聚合态仍由 `DashboardRun*` 七常量独立表达（多出的 `partial` 不属于作业态）。
3. 替换范围：7 个生产文件（postgres 的 query/query_worker/dashboard_runs/metrics_queries/reporting_queries/formula_metric_queries + reporting/dashboard_policy）与 21 个测试文件，全部为 Go 侧比较与 `Job.State` 赋值；diff 呈 73 增 73 删对称，无任何行为分支变化。
4. 刻意保留的字面量：
   - raw SQL 与迁移 005/后续迁移中的 `'queued'` 等：位于查询文本内部，由表级 CHECK 约束兜底，且与既有 backfill/export 作业的处理方式一致；
   - export/backfill/archive/retention/erasure/pipeline 各自的状态闭集：独立词汇（注意 backfill/export 使用双 l 的 `cancelled`），各自已有契约守卫；
   - 控制面测试中的 cookie `"expired"`、catalog release 的 `"failed"` 入参、Prometheus 指标标签文本：与查询作业态无关。

## 常驻守卫

新增 [job_state_contract_test.go](../../../backend/internal/analysis/job_state_contract_test.go)：`analysis.TestQueryJobStateVocabularyMatchesContract` 读嵌入 bundle（`wire.Schemas`），断言 `QueryJob.state` 枚举与 `JobStates` 排序后集合相等，零外部依赖。

## 守卫有效性取证（变异红绿）

- 基线 sha256 存 `/tmp/evia-check/queryjob-states/sha256-baseline.txt`；
- 向 `JobStates` 注入 `"drift_probe"` → 守卫报 `query job state drift: contract [canceled expired failed queued running succeeded] != Go [canceled drift_probe expired failed queued running succeeded]` 变红；
- 备份还原后 `sha256sum -c` 全部 OK，复跑复绿，`grep drift_probe/DriftProbe` 零残留。

## 验证

- `go vet ./...` 通过（含全部测试文件编译）；
- 全量 `make check` EXIT=0、零 FAIL（Go 32 包、client 51、sdk 63、console 18、生产构建），日志：`/tmp/evia-check/queryjob-states/make-check.log`；
- 真实 PostgreSQL 集成回归（`-tags integration`）：postgres 整包 103.929s 通过，覆盖 query 入队/领取/取消/过期、dashboard run 聚合与取消、metrics/reporting/formula/saved-analysis 版本入队与幂等重放等全部作业态流转；
- 唯一失败项 `TestFailoverBaselineWrite` 是既有环境门控（未设置 `EVIA_TEST_PG_FAILOVER_DSN` 即 t.Fatal，干净基线同样失败），以 `-skip` 排除后整包 ok，与本切片无关。

## 明确未做（边界）

- 不触碰 raw SQL/迁移内字面量与其他作业类型闭集；
- 不改变任何运行时行为、契约内容、API 形状或数据库约束；
- DashboardRun 七态常量保持独立，仅删除其重复维护的查询作业态副本。
