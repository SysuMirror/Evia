# 留存分析自然月粒度验证记录（2026-11-18）

## 问题（确定性缺口）

留存查询的粒度闭集此前只有 `day`/`week`，多份验证记录把「无自然月粒度」列为明确边界。跨月留存只能用周矩阵近似，无法直接得到「按月同期群」。本切片把 `month` 落地为第三个显式闭集成员，不新增查询类型、不改有界拉取护栏。

## 范围与语义决策

1. **时区对齐的自然月同期群**：每个队列对齐到请求时区下该自然月的首日 00:00；0 期恒为 1.0，回访按相对队列月的整数月偏移归集。月偏移用月序数（`year*12 + month`）计算，与变长月份无关，避免按天除法在 28/30/31 天月份上出错。
2. **扫描窗与日历对齐分离**：引擎内的队列归属完全基于日历月（`periodFloor`/`periodOffset`/`cohortStart`）；而校验阶段需要一个 `time.Duration` 上界来约束 `TimeRange`，月粒度采用保守的 **31 天/期**（12 期最多 372 天）。该上界只影响有界原始拉取的安全检查，不参与队列计算。
3. **期数闭集不变**：仍为 1–12 期；起止事件固定 `event_key`+`schema_version`、uid/anonymous 双主体、10 万行有界拉取等既有护栏全部沿用。
4. **planner/SQL 零改动**：留存计划只产出 `is_start`/`is_return` 标记列，日历逻辑全部在 Go 评估器，因此月粒度无需改 SQL，结果列契约（5 列、`exact`、水位）保持不变。
5. **即席与保存模板同源**：模板绑定走 `ValidateConfiguration` → `validateRetention`，放开一处闭集即同时覆盖即席查询与保存分析模板；负例仍用真正不支持的 `hour`，fail-closed 守护不被削弱。
6. **控制台同构**：即席分析与保存分析编辑器两个粒度选择器同步加「按月（自然月首日起算）」，时间窗提示与结果摘要按月口径渲染。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 契约 | `contracts/query/query-request.schema.json` | retention.granularity 枚举加 `month` 并补语义描述；重跑 codegen（Go 离线 bundle、TS `schema.gen.ts` 两处联合类型） |
| 领域 | `internal/analysis/validation.go`、`retention.go` | 闭集放开、`retentionWindow` 月分支（31 天上界）、`periodFloor` 月首日、`periodOffset` 月序数、`cohortStart` 用 `AddDate` 月进位 |
| 领域/集成测试 | `internal/analysis/retention_test.go`、`clickhouse_integration_test.go` | 2 个单测 + 1 个真实 ClickHouse 月队列测试 |
| 控制台 | `analysis-model.ts`、`analysis.tsx`、`saved-analysis-model.ts`、`saved-analysis-editor/retention.tsx`、`tests/browser/analysis-model.spec.ts` | 类型/选择器/窗口与摘要/闭集校验 + 1 个模型测试 |

## 验证

### 领域单测（`go test -race ./internal/analysis`）

新增 2 个用例 verbose PASS：

- `TestEvaluateRetentionMonthCohortMatrix`：2026-11 至 2027-01 三个月队列，断言跨年月偏移、变长月份下的 `cohort_index/cohort_start/cohort_size/retained/retention_rate`（11 月队列 2 人 `[2,1,1]`、12 月 `[1,1,0]`、1 月 `[1,0,0]`），并在测试内 `Validate` 正向确认 month 通过闭集。
- `TestEvaluateRetentionMonthAlignmentUsesTimezone`：Asia/Shanghai 下 UTC 16:30（本地次日 00:30）归入本地 11 月队列，`cohort_start` 为本地月首日折算的 UTC 时刻。

### 真实 ClickHouse 集成（`-tags=integration`，独立隔离库 + 真实迁移视图）

- `TestClickHouseRetentionMonthCohorts`（verbose PASS）：经迁移视图的双语有界拉取与 UInt8 标记解码，验证三个月队列、跨年月偏移、5 列结果契约与项目隔离（`project-two` 事实不可见）。

### 控制台

- Playwright 模型套件 104 项通过（新增自然月窗口/闭集用例：2 期月矩阵 62 天上界、12 期 372 天、外键粒度 `hour` 仍被拒绝）。
- `tsc --noEmit` 通过；prettier 全过；生产构建在 make check 内通过。

### 变异红绿（sha256 逐字节还原，禁用 git checkout）

后端（基线 `/tmp/evia-check/retention-month/SHA256SUMS`）：

1. 先尝试把月 `periodFloor` 改成按天午夜，发现被月序数偏移掩盖（弱变异，测试仍绿），如实放弃；改为把 `periodOffset` 月分支回退为按天序数 → 月队列偏移立即错乱（`cohort 0 offset 1 retained=0 want 1`）精确变红，证明月序数是跨月归集的必要逻辑。

前端（基线 `/tmp/evia-check/retention-month-fe/SHA256SUMS`）：

2. 把 `retentionSpanDays` 的月 31 天/期回退为按天 → 模型测试时间窗从 62 天塌缩为 2 天并精确报错变红。

每轮精确字符串还原后 `sha256sum -c` 全部 OK，复跑 tsc 与单测/模型测试复绿。

### 门禁

- `make check PYTHON=…/venv/bin/python` 一次通过（EXIT=0）：命名、契约、边界、监控、Go `-race` 全量包（analysis 15.7s）、prettier、typecheck、test:client 49、test:sdk 63、生产构建全绿。
- codegen 仅产生枚举扩展的预期 diff，无手改生成文件。

## 明确未做（边界）

- 仅新增自然月粒度；不提供周日起算、4-4-5 财季等自定义日历，周一口径与日粒度行为不变。
- 月扫描窗是 31 天/期的保守上界，真实日历月对齐只在评估器内完成；不改变 10 万行有界拉取与既有查询预算。
- 控制台无组件级单测设施，选择器交互以纯函数模型测试 + tsc + 生产构建为证据；全栈 browser-test 需凭证运行时，不在默认门禁。
- 月粒度的超大租户容量未做专项压测，沿用既有生产规模实测要求。
