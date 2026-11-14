# 主体分群 v1 后端垂直切片验证记录（2026-11-14）

## 范围

feature-coverage「留存、分群、聚合路径、归因……」中的分群此前未实现。
本切片交付主体级分群（`segment` 查询类型）后端闭环，与留存/漏斗同构，
不引入新存储、进程或依赖。控制台工作区在下一切片。

与既有 trend `group_by` 的关键差异：trend 是**事件级**分组，同一主体
可因不同事件的属性值跨组重复；segment 是**主体级**分群，每个主体按
其最早一条分群事件的标量字段值归入唯一群体，绝不重复计数。

## 交付内容

- 契约 `query-request.schema.json`：Kind 枚举增加 `segment`；新增
  `segment` 块（`event` 复用 retention_step 形状、`field` 引用新
  `$defs/segment_field`）；条件约束要求必带 subject 与 segment 块，
  禁止顶层 event_keys/event_versions/group_by/metrics/interval。
- Go 类型 `Segment`（types.go），严格解码器零未知字段接入。
- 校验（validation.go）：uid/anonymous 显式主体必填、分群事件固定
  event_key+schema_version、7 天有界窗口；字段标量类型限制
  （string/boolean/number/integer，拒绝 array/datetime）在编译期
  CheckResolved 执行；非 segment 携带 segment 块 fail-closed。
- 纯评估器 `segment.go`：每个主体取最早分群事件的字段值作为唯一
  标签（防御性按 event_time 比较，不完全依赖 SQL 排序）；缺失/空值
  归入固定 `__missing__` 组；输出群体去重主体数与占比，按主体数降序、
  同数按标签字典序排序。
- planner.go：funnel 同构有界拉取（100001 行、参数绑定、
  segment_value/segment_present 列、按 subject 独占 uid/anonymous_id）；
  clickhouse.go 执行分支与 10 万行预算；fields.go 字段遍历/类型解析覆盖。
- 能力发现广告 `segment` Kind，单测锁定。
- 生成制品（Go bundle/types、TS schema）随 codegen 重新生成。

无生产默认路径行为变更：既有五类查询 SQL 与校验逐字节不变。

## 语义口径（v1 冻结）

1. 主体为显式 `uid` 或 `anonymous`，缺主体 fail-closed；两标识按所选
   列独占计算、互不混合。
2. 群体标签 = 主体**最早**一条匹配分群事件的字段值；同一主体后续事件
   的不同值不会使其换组，保证唯一归属。
3. 字段缺失、null 或空字符串归入固定 `__missing__` 组，不伪造空群体。
4. 分组字段限定标量（字符串/布尔/数值）；数组、日期时间拒绝。
5. 输出 `segment_value`、`subjects`、`share`（组主体数/总分群主体数），
   按主体数降序、同数按标签升序。
6. 仅即席任务；保存分析模板显式拒绝 segment（`UNSUPPORTED_QUERY_KIND`）。

## 真实运行结果

环境：dev ClickHouse 容器（HTTP loopback 18123，用户 evia，凭据运行时
读取零落盘），隔离库 `evia_analysis_<uuid>` 应用全部真实迁移，结束
`DROP ... SYNC`。

两轮独立运行（直接 `go test -tags integration -run
TestClickHouseSegmentation` 与标准 `make query-test` 整包）均 ok：
单测 0.17s，整包 64.757s。实测断言：

| 群体 | 主体数 | 占比 | 说明 |
| --- | --- | --- | --- |
| pro | 2 | 0.5 | u1 最早 pro（后续 enterprise 不换组）、u2 |
| `__missing__` | 1 | 0.25 | u4 无 plan 属性 |
| enterprise | 1 | 0.25 | u3 |

跨项目 outsider 被作用域过滤；每轮后隔离库计数为 0，零残留。

单元测试覆盖：唯一归属（最早值胜出）、缺失/空值归组、布尔/数值标量
渲染、空拉取不伪造群体、校验闭集（schema 层与 Go 纵深防御分层）、
编译期 array/datetime 拒绝、planner 主体列互斥与参数绑定、模板拒绝。

## 先红后绿：过程中修复的真实问题

1. **类型校验层级错误**：初版在 ValidateConfiguration 检查
   ResolvedType，但该字段在编译期才由 catalog 填充，校验阶段为空会误拒。
   改为标量限制放 CheckResolved，Validate 只做语法与结构校验。
2. **测试排序预期错误**：同规模群体按标签字典序，`_`(95) 先于小写
   字母；按真实排序修正断言，而非修改实现。
3. **冗余 planner 调用**：初版多余构造 is_present 过滤器且丢弃结果，
   移除后直接复用 field() 的 present 表达式。
4. **生成制品依赖**：改契约后重跑 codegen 与 TS 生成，否则旧 bundle
   拒绝合法请求。

## 诚实边界

- 仅即席任务，无控制台工作区（下一切片）、无保存分析、无跨身份合并。
- 标签取最早事件值，不提供最近/众数等其他归属策略；高基数字段的群体
  数量受 500 行结果上限与 7 天窗口约束，超大租户容量未专项压测。
- 不支持数组/日期时间分组；分群是静态属性快照，不做群体的时间演化追踪。
