# 末次触点归因分析 v1 后端垂直切片验证记录（2026-11-14）

## 范围

feature-coverage「留存、分群、聚合路径、归因……」中的归因此前未实现。
本切片交付主体级末次触点归因（`attribution` 查询类型）后端闭环，与
漏斗/留存/分群/路径同构，复用现有事件数据与有界拉取+进程内 Go 评估器
架构，不引入新存储、进程或依赖。控制台工作区在下一切片。

口径区别：漏斗判定固定步骤是否转化，路径统计实际事件序列；归因回答
「转化应记到哪个触点」。v1 仅冻结**末次触点（last_touch）**一种模型，
模型为请求中的显式闭集枚举，后续新增首触/线性/衰减模型时不改请求形状、
不影响已发布结果。

## 交付内容

- 契约 `query-request.schema.json`：Kind 枚举增加 `attribution`；新增
  `attribution` 块（必填 `conversion` 与 `touchpoints` 复用 retention_step
  固定事件版本+可选过滤、`window_seconds` 1–604800、`model` 单值枚举
  `last_touch`，触点 1–8 个）；allOf 条件约束要求必带 subject 与
  attribution 块，禁止顶层 event_keys/event_versions/group_by/metrics/
  interval。
- Go 类型 `Attribution`（types.go），严格解码器零未知字段接入。
- 校验（validation.go）：uid/anonymous 显式主体必填、模型闭集 fail-closed、
  窗口 1–604800 秒、7 天有界时间范围、触点 1–8 个且（事件,版本）对去重、
  触点不得与转化事件相同、转化/触点固定版本与过滤 DSL 校验；非
  attribution 携带 attribution 块 fail-closed。
- 纯评估器 `attribution.go`：按主体分组，用与漏斗/路径一致的因果排序；
  以每个主体**最早一次转化**为唯一定位点（每主体最多一条归因），在
  `window_seconds` 回溯窗内取距转化最近的触点记其 event_key；窗内无触点
  的转化归入固定 `__direct__` 桶，保证转化主体不被静默丢弃；输出
  `touchpoint`/`subjects`/`share`（占转化主体分母），按主体数降序、同数
  按触点字典序。
- planner.go：拉取全部在窗主体事件（100001 行、参数绑定、按 subject 独占
  uid/anonymous_id），附加 `is_conversion` 与 `is_touchpoint`（触点 OR
  匹配）两个标记列，不下推 WHERE 以保留转化前后完整因果序列；
  clickhouse.go 执行分支与十万行守卫；fields.go 转化/触点过滤器遍历。
- 能力发现广告 `attribution` Kind，单测锁定。
- 生成制品（Go bundle/types、TS schema）随 codegen 重新生成。

无生产默认路径行为变更：既有七类查询 SQL 与校验逐字节不变。

## 语义口径（v1 冻结）

1. 主体为显式 `uid` 或 `anonymous`，缺主体 fail-closed；两标识按所选列
   独占计算、互不混合。
2. 转化事件与触点均钉选固定 event_key+schema_version，可带同一有界过滤
   DSL；触点与转化不得为同一（事件,版本）对，触点之间不得重复。
3. 每个转化主体只归因一次，定位点为其窗口内最早转化；归因模型 v1 仅
   `last_touch`，取回溯窗内因果序最晚的触点。
4. 回溯窗 `window_seconds`（1–604800，即最长 7 天）；窗内无任何触点的
   转化进入固定 `__direct__` 桶，分母仍为全部转化主体。
5. 输出 `touchpoint`、`subjects`（去重转化主体数）、`share`，按主体数
   降序、同数按触点字典序。
6. 仅即席任务；保存分析模板显式拒绝 attribution（`UNSUPPORTED_QUERY_KIND`）。

## 真实运行结果

环境：dev ClickHouse 容器（HTTP loopback 18123，用户 evia，凭据运行时
读取零落盘），隔离库 `evia_analysis_<uuid>` 应用全部真实迁移，结束
`DROP ... SYNC`。

两轮独立运行（直接 `go test -tags=integration -run
TestClickHouseAttribution` 0.222s 与标准 `make query-test` 整包 66.520s
`-race`）均 ok。实测断言：

| 触点桶 | 主体数 | 说明 |
| --- | --- | --- |
| __direct__ | 1 | u3 无触点直接转化 |
| ad_click | 1 | u2 唯一前置触点 |
| email_open | 1 | u1 两个触点中最晚者获胜 |

u4 无转化被排除；outsider 跨项目两事件被作用域过滤（转化主体恒为 3）；
三桶 share 均为 1/3。收紧窗口到 5 秒（所有触点与转化间隔均为 10 秒）后
三名转化主体全部塌缩为 `__direct__=3`，证明窗口边界真实生效。每轮后
隔离库计数为 0，零残留。

单元测试覆盖：末次触点获胜、直接桶、回溯窗包含/排除、同时间因果序、
空拉取、schema 与 Go 双层校验闭集（未知模型/越界窗口/空触点/缺主体/
触点与转化重叠/触点重复/超 8 触点/顶层 event_keys）、planner 主体列
互斥与双标记列、模板拒绝。

## 先红后绿：过程中修复的真实问题

1. **生成制品依赖**：改契约后 Go 校验器加载旧 bundle，重跑 codegen 与
   TS 生成后通过（固定步骤）。
2. **测试标记依赖时区脆弱 ID**：初版用 `time.Unix(...).Format` 拼出的
   event_id 反查标记，受进程时区影响未命中导致零归因；改为构造行时
   直接打 `is_conversion`/`is_touchpoint` 标记，去除脆弱映射。
3. **标记列不能下推 WHERE**：与路径切片同理，若只拉触点/转化事件会丢失
   判定所需的完整序列，改为全量拉取 + Go 内标记评估。
4. **event_versions 纵深防御漏项**：初版只更新了拒绝提示文案，漏把
   `attribution` 加入 validateEventSelection 的 Kind 闭集——schema 层已拦截
   外部请求，但 Go 层对内部构造的请求会漏；补齐条件并新增
   `top_level_event_versions` 子测试钉住该拒绝路径。

## 诚实边界

- 仅即席任务，无控制台工作区（下一切片）、无保存分析、无跨身份合并。
- v1 仅 last_touch 单模型；首次触点、线性均分、时间衰减等模型待后续作为
  显式模型版本扩展，不回改已发布结果。
- 归因基于原始 event_key 触点，不做渠道分组归并或多触点权重分摊；高基数
  触点数量受 500 行结果上限与 7 天窗口约束，超大租户容量未专项压测。
- 热力图、会话回放仍未实现，且需独立采集协议、脱敏与专用存储，不能从
  普通事件表推断。
