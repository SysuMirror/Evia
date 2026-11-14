# 聚合路径分析 v1 后端垂直切片验证记录（2026-11-14）

## 范围

feature-coverage「留存、分群、聚合路径、归因……」中的聚合路径此前未实现。
本切片交付主体级路径聚合（`path` 查询类型）后端闭环，与漏斗/留存/分群
同构，不引入新存储、进程或依赖。控制台工作区在下一切片。

口径区别：漏斗是固定步骤的转化判定（允许穿插），路径分析是统计主体
**实际遍历的有序事件序列**，用于发现真实行为流向，不预设步骤。

## 交付内容

- 契约 `query-request.schema.json`：Kind 枚举增加 `path`；新增 `path`
  块（可选 `start_event` 复用 retention_step、必填 `depth` 2–8）；条件
  约束要求必带 subject 与 path 块，禁止顶层 event_keys/event_versions/
  group_by/metrics/interval。
- Go 类型 `Path`（types.go），严格解码器零未知字段接入。
- 校验（validation.go）：uid/anonymous 显式主体必填、depth 闭集 2–8、
  7 天有界窗口、可选起始事件固定版本；非 path 携带 path 块 fail-closed。
- 纯评估器 `path.go`：按主体分组，用与漏斗一致的因果排序（event_time，
  同时间按会话/标签页/来源的递增 sequence）；有起始事件则取其最早匹配
  之后的 depth 个事件，否则取窗口内最早 depth 个；事件不足 depth 的主体
  排除；每主体最多贡献一条路径，按主体数降序、同数按路径字典序输出。
- planner.go：拉取全部在窗主体事件（100001 行、参数绑定、按 subject
  独占 uid/anonymous_id），有起始事件时附加 `is_start` 标记列（起始
  之后的事件仍在拉取集中以保留路径尾部）；clickhouse.go 执行分支与
  10 万行预算；fields.go 起始过滤器遍历。
- 能力发现广告 `path` Kind，单测锁定。
- 生成制品（Go bundle/types、TS schema）随 codegen 重新生成。

无生产默认路径行为变更：既有六类查询 SQL 与校验逐字节不变。

## 语义口径（v1 冻结）

1. 主体为显式 `uid` 或 `anonymous`，缺主体 fail-closed；两标识按所选
   列独占计算、互不混合。
2. 每条路径固定 `depth`（2–8）个事件；每主体最多贡献一条路径，绝不
   重复计数。
3. 起始事件可选：指定时从该主体最早匹配事件起取 depth 个；未指定时
   从窗口内最早事件起取。事件不足 depth 的主体不计入。
4. 同时间事件按共享会话的因果 sequence 排序，与漏斗口径一致。
5. 输出 `path`（event_key 有序数组）与 `subjects`（去重主体数），按
   主体数降序、同数按路径字典序。
6. 仅即席任务；保存分析模板显式拒绝 path（`UNSUPPORTED_QUERY_KIND`）。

## 真实运行结果

环境：dev ClickHouse 容器（HTTP loopback 18123，用户 evia，凭据运行时
读取零落盘），隔离库 `evia_analysis_<uuid>` 应用全部真实迁移，结束
`DROP ... SYNC`。

两轮独立运行（直接 `go test -tags integration -run
TestClickHousePathAggregation` 与标准 `make query-test` 整包）均 ok：
单测 0.21s，整包 65.270s。实测断言：

| 路径 | 主体数 | 说明 |
| --- | --- | --- |
| open → view → buy | 2 | u1、u2 同路径 |
| open → view → share | 1 | u3 |

u4 仅两个事件无法填满 depth-3 被排除；跨项目 outsider 三个事件被作用域
过滤；起始事件钉选 view 时路径以 view 开头并取 depth-2 尾部。每轮后
隔离库计数为 0，零残留。

单元测试覆盖：有序序列聚合、钉选起始、同时间因果 sequence、短主体
排除、空拉取、校验闭集（schema 层与 Go 纵深防御分层）、planner 主体列
互斥与 is_start 标记、模板拒绝。

## 先红后绿：过程中修复的真实问题

1. **生成制品依赖**：改契约后 Go 校验器仍加载旧 bundle 拒绝合法请求，
   重跑 codegen 与 TS 生成后通过（与既有切片一致的固定步骤）。
2. **起始事件不能下推 SQL 过滤**：若 WHERE 只留起始事件会丢失路径尾部，
   改为全量拉取 + is_start 标记列，在 Go 内定位起点后取后续事件。
3. **无用排序变量**：集成测试初稿残留未使用的 ordering 切片，清理避免
   死代码。

## 诚实边界

- 仅即席任务，无控制台工作区（下一切片）、无保存分析、无跨身份合并。
- 路径由原始 event_key 序列构成，不做连续重复事件折叠或会话切分；
  高基数下路径数量受 500 行结果上限与 7 天窗口约束，超大租户容量未
  专项压测。
- 不提供归因权重模型；归因、热力图、会话回放仍未实现。
