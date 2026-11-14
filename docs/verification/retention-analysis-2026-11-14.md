# 留存分析 v1 后端垂直切片验证记录（2026-11-14）

## 范围

feature-coverage 明确「留存、分群、聚合路径、归因、热力图和会话回放」
为未实现。本切片交付其中留存分析的后端闭环：在既有 funnel 同构架构上
新增 `retention` 查询类型，产出时区对齐的日历队列留存矩阵，不引入
新存储、新进程或新依赖。控制台 UI、api-client 业务方法、保存分析模板
与匿名主体留存在 v1 边界之外（见文末）。

## 交付内容

- 契约 `contracts/query/query-request.schema.json`：Kind 枚举增加
  `retention`；新增 `retention` 对象（`start_event`/`return_event`
  复用步骤形状、`granularity` 闭集 day/week、`periods` 1–12）与
  `$defs/retention_step`；条件约束要求 retention 必须携带 subject 与
  retention 块，且禁止顶层 `event_keys`/`event_versions`。
- Go 类型 `Retention`（types.go），请求严格解码器零未知字段接入。
- 校验（validation.go）：uid 单主体、粒度/期数闭集、起止事件固定
  event_key+schema_version、过滤器节点预算、时间窗不得超过
  `periods × granularity`（最大 84 天）；结构校验先于窗口推导，
  畸形值不会被窗口错误掩盖；非 retention 携带 retention 块 fail-closed。
- 纯评估器 `retention.go`：两轮扫描（先按最早 start 事件定队列，再
  映射 return 事件偏移）；周期在请求时区内按日历日/周一对齐周截断，
  用日历序数差而非纳秒除法，DST 不移动边界；0 期恒为 1.0，入队前
  return 与超界偏移忽略；无 start 的主体不入队。
- planner.go：funnel 同构的有界双语义拉取（100001 行上限、参数绑定、
  `is_start`/`is_return` UInt8 标志）；clickhouse.go 增加 retention
  执行分支与 10 万行预算；fields.go 字段遍历/类型解析覆盖两个步骤过滤器。
- 生成制品随契约重新生成：`contracts.gen.json`、`types.gen.go`、
  `packages/api-client/src/schema.gen.ts`。
- 能力发现（reporting/discovery.go 与 OpenAPI `AnalysisCapabilities`）
  广告 `retention` Kind、`retention_granularities` 及两个留存预算字段，
  单测锁定广告值；留存属聚合查询，`RequiresRawAccess` 为假，不索取
  `queries.raw.read` 权限。
- 测试：`retention_test.go` 表驱动单测（评估器 4 组、校验闭集 13 例、
  模板拒绝、planner SQL 断言）；`clickhouse_integration_test.go`
  新增 `TestClickHouseRetentionMatrix` 真实 ClickHouse 端到端。

无生产默认路径行为变更：既有四类查询的 SQL 与校验逐字节不变。

## 语义口径（v1 冻结）

1. 主体仅 `uid`；`anonymous` 与空主体以 `SUBJECT_REQUIRED` 拒绝，
   不做任何跨身份合并（身份图本身未实现）。
2. 队列由主体**最早** start 事件所在日历周期决定；周期 0 代表入队
   本身，留存率恒为 1.0。
3. return 事件必须显式指定 event_key 与版本；return 早于入队记为
   负偏移并忽略，偏移 ≥ periods 丢弃。
4. 粒度为请求时区对齐的日历日或周一起始周；期数 1–12。
5. 输出每行一个非空队列：`cohort_index`、`cohort_start`（UTC RFC3339）、
   `cohort_size`、`retained[]`、`retention_rate[]`，最多 12 行。

## 真实运行结果

环境：dev ClickHouse 8.x 容器 `evia-pipeline-dev-clickhouse-1`
（HTTP loopback 18123，用户 evia，凭据运行时从容器读取、零落盘），
隔离库 `evia_analysis_<uuid>` 应用全部真实迁移，结束 `DROP ... SYNC`。

命令（工具链激活后）：

```
EVIA_TEST_CLICKHOUSE_URL=http://127.0.0.1:18123 \
EVIA_TEST_CLICKHOUSE_USERNAME=evia \
EVIA_TEST_CLICKHOUSE_PASSWORD=<runtime> \
  make query-test
```

两轮独立运行（一轮直接 `go test -tags integration`、一轮标准
`make query-test`）整包均 ok：65.652s / 66.243s，新测试单测耗时
0.23s。矩阵断言（day 粒度、4 期、UTC）：

| 队列 | 规模 | retained | 说明 |
| --- | --- | --- | --- |
| 2026-11-07（index 0） | 2 | [2,0,1,1] | u1 第 2 期回访、u2 第 3 期回访；入队前 return 不计负偏移 |
| 2026-11-08（index 1） | 1 | [1,1,0,0] | u3 次期回访 |
| 2026-11-09（index 2） | 1 | [1,0,0,0] | u4 仅入队 |

另有仅 return 无 start 的 u5 被排除、跨项目 outsider 被作用域过滤。
每轮后 `system.databases LIKE 'evia_analysis_%'` 计数为 0，零残留。

单元测试覆盖的关键性质：周一对齐的 Asia/Shanghai 周队列（UTC 前日
16:30 的事件仍属本地周一队列）、0 期恒 1.0、UInt8/bool 标志等价、
最早 start 定队、空拉取不伪造队列、损坏时间 fail-closed、JSON schema
与 Go 双层校验的错误码分层。

## 先红后绿：过程中修复的真实问题

1. **schema 条件语义错误**：初版用 `not: {required: [event_versions,
   event_keys]}`，JSON Schema 中多键 required 仅在两者同时出现时触发，
   单带 event_keys 漏过 schema 直达 Go 层。改为 `not.anyOf` 两个
   单键 required，契约层即拒绝；Go 层保留同名 fail-closed 作为内部
   请求的纵深防御。
2. **校验顺序掩盖配置错误**：窗口先于结构校验计算时，畸形 granularity
   会先触发 `QUERY_COST_EXCEEDED` 而非真正的
   `INVALID_RETENTION_GRANULARITY`。改为 retention 先跑幂等的
   ValidateConfiguration 再推导窗口。
3. **生成制品依赖**：Go 校验器加载的是生成 bundle 而非源 schema，
   改契约后必须重新跑 codegen 与 `pnpm generate:api`，否则合法请求
   被旧 bundle 拒绝；已通过 `make generate` 链路同步三处制品。
4. **测试类型断言**：评估器结果不经过 JSON 往返，retained 是原生
   `[]int`、rate 是 `[]any` 的 float64，初版误按 json.Number 断言，
   按真实类型修正。

## 控制台工作区（同日前端切片）

后端能力通过即席分析工作区对人类会话开放：

- `analysis-model.ts` 新增 `retention` 模式与表单字段（起止事件+固定
  版本、day/week 粒度、1–12 期），`buildRequest` 产出 uid 单主体请求，
  前端预校验时间窗 ≤ 期数×粒度（与后端闭集一致，84 天硬顶），错误
  归因到独立的 `retention` 编辑分区。
- `analysis.tsx` 新增「留存分析」页签、留存配置模态（复用已发布事件
  选择器与版本联动）、结果表队列列头与数组列（各期留存数/留存率）
  中文渲染，结果摘要显示期数与粒度；留存模式隐藏跨版本事件选择器与
  主体选择器（主体固定 uid）。
- `tests/browser/analysis-model.spec.ts` 新增两个纯模型测试（请求
  形状、闭集校验、窗口随粒度/期数变化、84 天硬顶），6 个用例全过；
  `core-console.spec.ts` 全栈流程新增留存页签→配置→运行→队列矩阵
  断言（100.00% 0 期留存可见），随凭证化 browser-test 门禁执行。
- pnpm typecheck、format:check、build 均通过。

## 诚实边界

- v1 仅 uid 主体、仅即席任务：保存分析模板显式拒绝 retention
  （`UNSUPPORTED_QUERY_KIND`），匿名主体、分群、归因不在本切片。
- 控制台全栈规格需凭证化完整运行时（browser-test 门禁），默认
  `make check` 不含该门禁；前端逻辑由模型单测、后端由真实 ClickHouse
  集成测试分别覆盖。
- 有界拉取沿用 funnel 的 10 万行/4MiB 响应护栏与 ClickHouse 读护栏，
  84 天窗口下的超大租户容量未做专项压测，需生产规模实测。
- 日历周期按请求时区计算，周一口径固定；未提供周日起算或自然月粒度。
- 集成测试经迁移视图直连引擎，公开 HTTP 入队到 worker 落库的链路
  由既有查询作业证据覆盖，本切片不重复验证。
