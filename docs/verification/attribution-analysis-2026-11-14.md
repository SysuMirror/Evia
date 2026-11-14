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

## 控制台工作区（同日切片）

归因后端能力通过即席分析工作区对人类会话开放：

- `analysis-model.ts` 新增 `attribution` 模式与表单字段（转化事件+版本、
  1–8 个触点事件+版本、回溯窗口秒），`readAttribution` 校验转化与触点固定
  版本、触点 1–8 且（事件,版本）去重、不得与转化同事件、窗口为 1–604800
  整数，模型按表单选择 `last_touch`/`first_touch`（后端枚举为准，默认末次），错误归因独立
  `attribution` 编辑分区；`buildRequest` 透传 uid/anonymous 主体，禁顶层
  event_keys/event_versions/group_by/metrics/interval。
- `analysis.tsx` 新增「归因分析」页签与配置模态：复用已发布事件/版本联动
  选择器，触点支持增删（1–8），结果表渲染归因触点（含 `__direct__` 直接
  来源桶）、转化主体数与归因占比，摘要显示末次触点模型与窗口，查询栏隐藏
  跨版本事件选择器并给出主体选择与 7 天窗口说明。
- 模型测试新增 `attribution builds last-touch credit requests` 覆盖请求
  形状、双主体、缺转化/触点重叠/触点重复/窗口闭集/超 8 触点拒绝、分区
  校验；Playwright 模型套件 90 例全过。`core-console.spec.ts` 全栈流程先用
  API 发布第二个未激活但已发布的事件（分析授权只校验 release 归属不要求
  激活）作为触点，再走归因页签→配置转化/触点/窗口→运行→末次触点摘要与
  `__direct__` 单元格断言，随凭证化 browser-test 门禁执行。
- pnpm typecheck、format:check、build 均通过。

## 首次触点模型扩展（opt-in 模型版本）

模型枚举本就为扩展设计，本次在不改请求形状、不影响已发布 last_touch
结果的前提下加入 `first_touch`：

- 契约 `attribution.model` 枚举扩展为 `last_touch`/`first_touch`，重跑
  codegen 三处制品；Go 校验放开为显式闭集，未知模型仍 fail-closed
  （`INVALID_ATTRIBUTION_MODEL`）。
- 评估器按模型选择扫描方向：first_touch 在回溯窗内**前向**取最早触点，
  last_touch 维持**后向**取最晚触点；窗口外触点在两个模型下都不计入，
  无窗内触点时两者都归固定 `__direct__` 桶。转化定位点（每主体最早转化、
  最多一条归因）、share 分母与排序口径完全不变。
- 单测新增 3 例：同批 ad_click→email_open→buy 证据下两模型归因分歧、
  窗口边界（紧窗跳过窗外首触、宽窗取最早）、契约与 Go 双层模型闭集；
  同时把原先以 `first_touch` 充当「未知模型」负例的两处断言改为真正未
  实现的 `linear`/`time_decay`，避免合法值被误判。
- 真实 ClickHouse 新增 `TestClickHouseFirstTouchAttribution`，与
  last_touch 同批证据对照：u1 的两个触点在首触下改记到最早 ad_click，
  email_open 桶消失，结果为 ad_click=2（share 2/3）、__direct__=1；
  直接 go test 0.448s 与整包 `make query-test` 67.123s `-race` 两轮零残留。
- 控制台归因模态新增「归因模型」选择器（默认末次触点），表单
  `attributionModel` 经 `readAttribution` 透传，概览与结果摘要按模型显示；
  模型套件 92 例全过。

## 线性均分（linear）多触点权重模型

在两种单触点全权重模型之上加入等权多触点模型，仍是显式模型枚举的一员，
不改变请求形状，且只对 linear 输出附加加权列，单触模型结果字节稳定：

- 契约 `attribution.model` 枚举扩展为 `last_touch`/`first_touch`/`linear`，
  重跑 codegen；Go 校验闭集同步放开，未知模型（如 time_decay）仍 fail-closed。
- 评估器重构：抽出 `loadAttributionSubjects` 复用主体加载，linear 走独立的
  `evaluateLinearAttribution`，单触路径原样保留。语义：每个转化主体把单位
  权重在窗内**去重后的触点 event_key** 间均分（N 个各得 1/N），同一触点
  多次出现只算一个节点；窗内无触点的转化把完整权重 1 留在 `__direct__`。
- 输出在 touchpoint/subjects/share 之外仅对 linear 增加 `credit`（float，
  权重累加）；`subjects` 仍是到达该触点的去重转化主体数，`share =
  credit/转化主体数`，全部行 share 之和恒为 1（浮点容差 1e-12）。按 credit
  降序、触点字典序排序。
- 先红后绿两处：① 旧负例再次把已合法的 `linear` 当未知模型，统一改回仍未
  实现的 `time_decay`；② share 求和断言因浮点累加得 0.9999999999999999，
  改为 1e-12 容差（属测试精度，非生产缺陷）。
- 单测新增 4 例：均分+直接桶（ad_click credit 1.5、email_open 0.5、direct
  1.0，share 和为 1）、同键重复去重、窗口排除、契约与 planner 列契约
  （linear 四列、单触仍三列）。真实 ClickHouse 新增
  `TestClickHouseLinearAttribution` 验证四列与加权值，直接 go test 0.622s
  与整包 `make query-test` 67.775s `-race` 两轮零残留。
- 控制台模型选择器加「线性均分」，结果表渲染 `credit`（最多 4 位小数并去
  尾零），概览/摘要显示线性均分；模型套件 93 例全过。

## 时间衰减（time_decay）指数权重模型

在等权多触点模型之上加入时间加权，仍是显式模型枚举成员。半衰期不是
硬编码产品常量，而是请求内显式有界整数参数 `decay_half_life_seconds`
（1–604800 秒，与窗口同界），仅 time_decay 必填、其他模型忽略：

- 契约 model 枚举加 `time_decay` 并新增可选半衰期间字段，重跑 codegen；
  Go 校验在 model=time_decay 时强制半衰期落在闭集内，否则
  `INVALID_ATTRIBUTION_HALF_LIFE` fail-closed，非衰减模型不要求该字段。
- 权重采用业界标准指数半衰公式：触点距转化 delta 秒的原始权重
  `0.5^(delta/half_life)`，越接近转化权重越高；同一 event_key 多次出现取
  最晚（最小 delta、最高权重）实例；主体内对原始权重归一化使总和恰为 1，
  无窗内触点的转化仍把完整权重 1 留在 `__direct__`。输出与 linear 同为四列
  （含 float `credit`），share 各行之和恒为 1（容差 1e-12）。
- 先红后绿：前两次模型扩展把 `time_decay` 暂作「未知模型」负例，本切片把
  三处统一改为真正未实现的 `position_based`，避免合法值被误判。
- 单测新增 5 例：归一化到 1/3 与 2/3 且更近触点权重更高、同键取最晚实例、
  窗口排除与直接桶、半衰期缺失/越界 fail-closed 且单触模型不要求该字段、
  planner 四列契约。真实 ClickHouse 新增
  `TestClickHouseTimeDecayAttribution`，10 秒半衰期满血验证
  ad_click=1/3、email_open=2/3、direct=1，直接 go test 0.352s 与整包
  `make query-test` 68.025s `-race` 两轮零残留。
- 控制台仅在选择时间衰减时显示半衰期输入，readAttribution 做 1–604800
  整数闭集校验并仅对该模型输出字段，单触模型请求不含半衰期；选择器、
  概览与摘要加入「时间衰减」，模型套件 94 例全过。

## 诚实边界

- 仅即席任务，无保存分析、无跨身份合并；控制台全栈规格需凭证化完整运行时
  （browser-test 门禁），默认 `make check` 不含该门禁。
- 已支持 last_touch、first_touch 单触全权重、linear 等权多触与
  time_decay 指数时间加权（半衰期为请求内有界参数）四种模型；其他位置
  基础（position-based 等）模型待后续作为显式模型版本扩展，不回改已发布结果。
- 归因基于原始 event_key 触点，不做渠道分组归并或多触点权重分摊；高基数
  触点数量受 500 行结果上限与 7 天窗口约束，超大租户容量未专项压测。
- 热力图、会话回放仍未实现，且需独立采集协议、脱敏与专用存储，不能从
  普通事件表推断。
