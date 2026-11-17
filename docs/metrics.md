# 版本化指标

指标把一套统计口径保存成独立资源：固定事件版本、行级筛选、统计主体、聚合、精度和单位。服务端支持创建草稿、修订比较、差异校验、不可变发布和指定版本运行。普通 UI 和 Agent 使用相同 API。

当前对外能力是原子指标。复合指标公式的纯领域引擎（受限有类型 AST、依赖 DAG、单位/null/除零语义与多指标对齐执行）已在后端实现并通过单元测试，但 OpenAPI 契约、PostgreSQL 持久化、查询工作进程接入、HTTP 接口与控制台尚未接通，因此当前没有可调用的公式接口，也不改变现有原子指标与查询路径。单位换算、看板直接引用指标版本仍待交付。现有保存分析可固定引用不可变指标版本，已有看板不会被新指标或指标草稿修改。

## 统计口径

| 聚合 | 单位与含义 |
| --- | --- |
| `count` | `event`，选定事件的次数；只有明确绑定业务页面事件时才表示业务 PV |
| `unique`，未指定字段，`subject: uid` | `raw_uid`，原始登录标识去重，排除缺失/空 UID |
| `unique`，未指定字段，`subject: anonymous` | `anonymous_id`，匿名标识去重，不代表已经合并为自然人 |
| `unique`，指定字段 | `distinct_value`，该字段的不同值数量 |
| `sum`、`avg`、`min`、`max`、`p95` | 必须指定支持的数值字段，单位为 `number`、`millisecond`、`second` 或 `byte` |

默认精确，只有 `unique` 和 `p95` 可以显式选择 `accuracy: approximate`。单位与聚合不兼容时拒绝保存。数值单位由作者声明，服务端不会自动推断业务含义，也不会把毫秒转换成秒；修改单位会产生发布 diff。当前没有货币及复合量纲运算。

每个指标固定 1–20 个已发布事件版本，每个事件只选一个版本。草稿可以先引用待发布版本，但校验和发布必须证明这些版本属于当前项目与环境，并校验属性类型。历史版本查询不要求该事件仍是当前激活版本。系统事件运行时注册及身份图仍未实现。

## 接口与发布流程

令 `P = /api/v1/projects/{project_id}`，`M = P/metrics/{metric_id}`。权威字段见 [OpenAPI](../contracts/http/openapi.json)。

| 方法与路径 | 行为 | 机器权限 |
| --- | --- | --- |
| `GET P/metrics` | 按必填 `environment` 分页，`limit` 默认 20，最大 100；可选 `view`（`active`/`archived`，默认 `active`）与 `q` 标题搜索 | `metrics.read` |
| `POST P/metrics` | 创建草稿，必需 `Idempotency-Key`，返回 201 | `metrics.write` |
| `GET M`、`GET M/draft` | 读取资源或草稿 | `metrics.read` |
| `PATCH M/draft` | 替换完整 definition，必需 `If-Match: "revision"` | `metrics.write` |
| `POST M/validations` | 返回规范化定义、事件依赖、diff 和校验摘要，不执行查询 | `metrics.write` |
| `GET M/versions`、`GET M/versions/{version}` | 读取不可变版本，版本列表默认 20，最大 50 | `metrics.read` |
| `POST M/versions` | 发布指定草稿，必需 `Idempotency-Key`，返回 201 | `metrics.publish` |
| `POST M/versions/{version}/queries` | 指定版本异步入队，必需 `Idempotency-Key`，返回 202 查询任务 | `metrics.run` 和 `queries.run` |
| `POST M/archive`、`POST M/unarchive` | 软归档/恢复，无请求体，200 返回带归档标记的资源；409 表示状态冲突 | `metrics.write` |

## 软归档生命周期（迁移 `031`）

`031_metric_archive.sql` 给 `metrics` 增加可空 `archived_at`/`archived_by` 列、活跃部分索引 `metrics_active_scope` 与标记 soundness CHECK。既有 `metric_preserve_scope` 触发器只保护 project/environment/metric_id/created_at，因此标记更新无需改触发器。`POST M/archive` 设置两个标记并把指标移出默认 `GET P/metrics` 列表；传 `view=archived` 列出已归档指标，`view=active`（或缺省）为默认流。归档可经 `POST M/unarchive` 逆转并清空标记。两个动作都要求 `metrics.write`、无请求体，并写审计 `metric.archived`/`metric.unarchived`。重复归档返回 409 `METRIC_ALREADY_ARCHIVED`，对活跃指标恢复返回 409 `METRIC_NOT_ARCHIVED`。

归档指标按 ID、草稿与不可变版本仍可读；归档期间草稿保存、校验快照、发布与新运行入队统一 409 `METRIC_ARCHIVED` fail-closed，已建查询作业仍可查询/取消。不可变版本是事实：保存分析的 `metric_ref` 与复合公式指标的依赖 DAG 都通过外键哈希见证引用 `metric_versions` 不可变表、从不回读可变父行，因此归档父指标不会破坏既有保存分析/公式指标版本及其运行；标记为可选可空字段，活跃指标省略。列表游标按 actor/project/environment 再绑定 `active`/`archived` 视图，跨视图复用一律 `INVALID_CURSOR`；未知 `view` 值返回 `INVALID_ARCHIVE_VIEW`。

## 标题搜索

指标列表接受可选 `q` 查询参数，按大小写不敏感的标题子串过滤，契约与看板/保存分析完全一致：边界去首尾空白，超过 128 rune 返回 `INVALID_QUERY`（`/q`），省略或空白即不过滤。服务端转义 `\`、`%`、`_` 并在存储侧声明 `ESCAPE '\'`，因此元字符按字面量匹配。搜索与环境、归档谓词合取且不跨环境；归一化词进入签名游标作用域，跨词分页返回 `INVALID_CURSOR`。复合公式指标列表仍不接受 `q`，发送即 `INVALID_QUERY`。控制台提供 300ms 防抖搜索框、镜像 128 rune 钳制、空白省略 `q` 并在换词时回到第一页。

人类 owner/editor 可编辑、校验和发布；viewer 可读取和运行聚合。会话写请求仍需 CSRF。读取查询任务/结果使用现有查询接口和 `queries.read`；涉及身份字段时额外检查 `queries.raw.read`，不能通过指标筛选或分组绕过。机器创建后的权限不可变，原 `analyses.*` 权限不隐含新增的 `metrics.*`。

创建示例使用已发布的合成页面事件：

```json
{
  "environment": "development",
  "definition": {
    "title": "页面登录访客",
    "description": "按业务页面事件统计原始 UID",
    "event_versions": [{ "event_key": "page_opened", "schema_version": 1 }],
    "subject": "uid",
    "aggregation": { "op": "unique", "accuracy": "exact" },
    "unit": "raw_uid"
  }
}
```

读取草稿得到 `draft_revision`、`published_version` 后，用 `{draft_revision, expected_published_version}` 请求校验。检查 diff，再把同一组版本和 `validation_hash` 提交给发布接口。发布事务重新读取草稿、依赖和基线，不把摘要当作授权。过时修订或基线返回 409，需要重读并重新校验。

创建和发布重试返回最初接受的快照；相同幂等键配不同内容返回冲突。响应丢失时保留原请求和幂等键。草稿更新通过修订比较防止覆盖，遇到未知结果先读取当前草稿。发布不会改写旧版本，也不会激活事件来源。

## 执行与结果

```json
{
  "time_range": {
    "from": "2026-11-01T00:00:00Z",
    "to": "2026-11-08T00:00:00Z"
  },
  "timezone": "Asia/Shanghai"
}
```

省略 `interval` 计算整个时间范围的聚合；加入 `interval: day`、`hour` 或 `minute` 得到趋势。整段 UV 必须直接运行整段查询，不能相加每日 UV。分组之间也可能包含相同身份，分组 UV 之和不是总体 UV。

可附加最多 3 个 `group_by` 字段、一个 `filter` 和 `limit`。实际筛选是“指标固定筛选 AND 本次筛选”，运行者不能删掉固定约束。字段仍需属于选定的已发布版本；合并后的条件树统一受现有深度、节点及参数预算约束。

复用现有异步查询引擎、去重事实、任务取消和权限复查。时间是 `[from,to)`，最多 31 天；结果默认 200 行、最多 500 行，当前没有结果续页。每项目共用最多 4 个运行任务和 32 个排队任务，没有独立指标队列绕过预算。轮询、结果及取消路径仍为 `P/queries/{query_id}` 和 `/result`。

执行重试标记保存 7 天，返回原任务的当前状态；标记有效但任务已过期或删除时返回 `QUERY_REQUEST_EXPIRED`，需新幂等键。原始机器凭据会保存在任务中，领取、执行租约及完成写入继续复查；轮换后的新凭据不能替旧任务续权。已完成结果按当前读取者的查询权限读取。

响应沿用查询结果的精度、生成时间、覆盖及水位元数据。当前覆盖仍为 `provisional`，观测到的最晚接收时间不是完整性证明，空桶不能自动解释为业务没有发生。

## 复合指标公式（全链路已打通）

公式（复合）指标把多个已发布的不可变指标版本，通过受限的有类型表达式组合成新口径。当前已全链路打通：纯领域引擎、持久化生命周期（迁移 `013`/`014`/`033`）、完整 OpenAPI 契约与代码生成、`FormulaMetrics` 服务、注册在 `/api/v1/projects/{project_id}/formula-metrics` 的完整 HTTP 接口（含运行入队）、查询工作进程复合扇出执行、`@evia/api-client` TypeScript 客户端，以及 React 控制台「复合公式指标」工作区（列表、双操作数表达式编辑器、校验/发布、版本与运行轮询），并以真实 PostgreSQL 竞态集成测试、契约正反向用例、HTTP 边界测试、客户端单测、控制台模型测试与类型检查验证。复合指标可继续引用其他已发布复合指标形成嵌套 DAG（迁移 `033` 起），该能力不改变现有原子指标与查询路径。显式单位换算已交付封闭的毫秒↔秒 `convert` 节点；更广义的量纲（如字节倍率、币种）与看板直接引用指标版本仍待交付。现有保存分析可固定引用不可变指标版本，已有看板不会被新指标或指标草稿修改。

### 受限表达式与预算

表达式是显式树，没有字符串语法、SQL 或 JavaScript，节点只有四类：

- `constant`：安全范围内的有限数值常量；
- `metric`：固定 `metric_id` 与 `version` 的不可变指标引用；
- `add`、`subtract`、`multiply`、`divide`：二元运算；
- `convert`：一元单位换算，只携带一个子表达式（`left`）与 `target_unit`，把同一物理量重新表达为另一个同量纲单位。

结构性预算：树深度最多 8、节点最多 64、单个表达式内不同指标引用最多 8。跨指标依赖另有限制：依赖链深度最多 4（只引用原子指标的公式深度为 1），完全展开后的不同原子指标最多 32，重复引用只计一次。归一化返回独立深拷贝，拒绝未知节点、不安全常量、自引用以及超出预算的表达式或依赖图。嵌套复合指标复用同一组预算，不需要额外限额。

### 依赖 DAG 与单位

`FormulaMetricResolver` 在授权事务内加载不可变指标版本（同项目/环境、`metrics.read`、禁止隐式解析到最新版本）：引用命中 `formula_metric_versions` 时返回复合节点（带其直接引用清单与公式单位），否则按原子指标版本解析。领域层据此执行三态 DFS，拒绝循环、自引用、过深依赖与展开超预算。单位采用保守的显式代数而非开放量纲系统：

- 加减要求两侧单位一致，结果单位不变；
- 乘法仅允许无量纲 `number` 缩放带单位量，两个带单位量相乘拒绝；
- 除法中相同单位相除得到无量纲比值（如转化率），带单位量除以 `number` 保留原单位，其余组合拒绝；
- `convert` 仅允许封闭换算表内的同量纲转换，当前只有 `millisecond↔second`（毫秒→秒乘 `0.001`，秒→毫秒乘 `1000`）；字节、事件、UV、无量纲等没有定义线性因子的单位一律拒绝，恒等转换（如毫秒→毫秒）也不在表内。

声明的结果单位必须与按依赖推导的单位一致；普通单位标签只做标注、不做任何数值换算，只有显式 `convert` 节点会按封闭表改变数值。换算因子在求值时仅由目标单位决定（单位推断已保证来源单位是唯一对应的同量纲单位），null 子值向上传播，换算结果越出安全整数范围同样归为 null。

### null、除零与对齐

标量以可空 float64 表示：任一操作数缺失即向上传播 null；除数为零（含负零）结果为 null，绝不伪装成 0；运算产生 NaN、无穷或超出安全整数范围也归为 null，响应不会携带非法数值。

多个叶子指标结果按时间桶（`bucket`）与分组维度（`group_*`）对齐：叶子缺少某个对齐键时该操作数为 null 而非 0，分组的 present 辅助列原样保留。复合结果沿用共享列结构，仅重算 `metric_0`；精度取所有叶子中最保守者（任一近似即近似），水位取最晚值，覆盖仍为 `provisional`。

`FormulaExecutor` 复用现有 `analysis.Engine`，每个不同引用只执行一次（AST 中重复引用去重），任一叶子失败即中止整个复合指标，执行错误不会被当成 null 吞掉。

### 持久化与不可变生命周期（迁移 `013`）

复合指标是独立聚合，复用原子指标的草稿、校验、不可变发布、版本与幂等语义，但落在专用表中，完全不改动原子指标的表结构与热路径：

- `formula_metrics` 保存可变草稿，定义 CHECK 强制 `formula` 形态且禁止携带 `event_versions`/`aggregation`；
- `formula_metric_versions` 保存不可变版本、依赖清单与三类摘要，触发器禁止更新或删除；
- `formula_metric_dependencies` 以关系外键把每个直接引用固定到某个不可变版本。迁移 `033` 起见证改为双目标：原子引用经 `ref_atomic_*` 外键指向 `metric_versions`，复合引用经 `ref_formula_*` 外键指向 `formula_metric_versions`，soundness CHECK 保证两行键恰有一个目标且与通用身份列一致，数据库层即保证被引用版本已发布且属于同项目/环境，无法固定草稿或跨作用域指标。

校验快照在授权事务内通过 PG 版 `FormulaMetricResolver` 完成 DAG 解析（同项目/环境、`metrics.read`、禁止隐式最新版本，复合节点返回其直接引用清单），再由纯函数 `BuildFormulaMetricValidation` 派生依赖清单、差异与摘要；发布事务重算校验并核对 `validation_hash`，写入版本与依赖见证，见证外键目标按校验图中的原子叶子集合选择。创建/发布复用 `metric_idempotency`，操作名以 `formula_` 前缀隔离。复合指标引用复合指标即嵌套 DAG，受同一组深度/展开预算约束；运行入队、HTTP 与控制台均已接入。

### HTTP 管理接口

令 `F = /api/v1/projects/{project_id}/formula-metrics`。复合指标是独立资源，与原子指标的 `/metrics` 路径分离，但复用同一组 `metrics.*` 权限，不新增机器授权项：

| 方法与路径 | 行为 | 机器权限 |
| --- | --- | --- |
| `GET F` | 按必填 `environment` 分页，`limit` 默认 20、最大 100；可选 `view`（`active`/`archived`，默认 `active`） | `metrics.read` |
| `POST F` | 创建草稿，必需 `Idempotency-Key`，返回 201 | `metrics.write` |
| `GET F/{metric_id}`、`GET F/{metric_id}/draft` | 读取资源或草稿（草稿带 `ETag` 修订） | `metrics.read` |
| `PATCH F/{metric_id}/draft` | 替换完整 definition，必需 `If-Match: "revision"` | `metrics.write` |
| `POST F/{metric_id}/validations` | 返回规范化定义、指标依赖、diff 与校验摘要，不执行查询 | `metrics.write` |
| `GET F/{metric_id}/versions`、`GET F/{metric_id}/versions/{version}` | 读取不可变版本，版本列表最大 50 | `metrics.read` |
| `POST F/{metric_id}/versions` | 发布指定草稿，必需 `Idempotency-Key`，返回 201 | `metrics.publish` |
| `POST F/{metric_id}/versions/{version}/queries` | 指定不可变版本异步入队，必需 `Idempotency-Key`，返回 202 `QueryJob` | `metrics.run`（并复查 `queries.run`） |
| `POST F/{metric_id}/archive`、`POST F/{metric_id}/unarchive` | 软归档/恢复，无请求体，200 返回带归档标记的资源；409 表示状态冲突 | `metrics.write` |

写请求继续受 CSRF、未知参数拒绝、严格 JSON（禁重复键/尾随值/未知字段）约束；契约层与服务层双重校验公式形态。运行请求复用 `MetricExecute` 契约（≤31 天、IANA 时区、`minute/hour/day` 粒度、最多 3 个分组、≤500 行），入队后按普通查询任务经 `P/queries/{id}` 轮询与取结果。

### 软归档生命周期（迁移 `032`）

`032_formula_metric_archive.sql` 给 `formula_metrics` 增加可空 `archived_at`/`archived_by` 列、活跃部分索引 `formula_metrics_active_scope` 与标记 soundness CHECK。既有 `formula_metric_preserve_scope` 触发器只保护 project/environment/metric_id/created_at，标记更新无需改触发器。`POST F/{metric_id}/archive` 设置两个标记并把复合指标移出默认 `GET F` 列表；`view=archived` 列出已归档项，`view=active`（或缺省）为默认流。归档可经 `POST F/{metric_id}/unarchive` 逆转并清空标记。两个动作都要求 `metrics.write`、无请求体，写审计 `formula_metric.archived`/`formula_metric.unarchived`。重复归档返回 409 `FORMULA_METRIC_ALREADY_ARCHIVED`，对活跃指标恢复返回 409 `FORMULA_METRIC_NOT_ARCHIVED`。

归档复合指标按 ID、草稿与不可变版本仍可读；归档期间草稿保存、校验快照（覆盖发布路径）与新运行入队统一 409 `FORMULA_METRIC_ARCHIVED` fail-closed，已建查询作业仍可查询/取消。不可变版本是事实：复合指标对原子指标的依赖经 `formula_metric_dependencies` 外键哈希见证引用 `metric_versions` 不可变表、从不回读可变父行，因此归档复合父指标不破坏其已发布版本与历史运行，被其引用的原子指标归档同理不受影响。列表谓词仅由校验后布尔量在两个常量间选择，请求文本永不进 SQL；游标按 actor/project/environment 再绑定 `active`/`archived` 视图，跨视图复用一律 `INVALID_CURSOR`，未知 `view` 值返回 `INVALID_ARCHIVE_VIEW`。

### 执行内核与扇出（迁移 `014`，worker 已接通）

入队与执行复用共享 `analysis_query_jobs`、项目锁和 4 运行/32 排队预算，不新增独立队列。迁移 `014` 的 `formula_metric_query_requests` 只保存幂等键、请求摘要与到复合不可变版本、查询任务的外键见证，触发器保证不可变。

复合任务在任务请求中携带一个**内部标记** `formula_ref`（`analysis.FormulaJobRef`），任务 `kind` 仍为 `trend`，因此无需放宽作业表约束。该字段不在公共查询契约中（根 schema `additionalProperties:false`），外部请求无法注入，只有内部入队路径写入。查询工作进程装配 `CompositeFormulaEngine`：普通请求原样委托 ClickHouse 引擎；遇到标记请求时经 `LoadFormulaExecution` 在 worker 上下文把根版本**递归展开**为 `FormulaExecutionPlan`——每个复合节点（根与嵌套子公式）记录其规范化公式，每个不同原子叶子用同一 `MetricTemplate` 构建一个趋势请求；引擎对原子叶子逐一扇出（共享叶子只执行一次），再由纯函数 `EvaluateFormulaPlan` 按 DAG 自底向上逐级 `EvaluateFormulaResult`，按时间桶/分组对齐并应用 null、除零与单位语义，计划内意外环仍 fail-closed。任一叶子失败即中止，错误不会被当成 null。

## 部署与存储

用新二进制显式执行迁移 `012_metrics.sql`，再启动 API 和工作进程。PostgreSQL 保存指标草稿、不可变版本、事件依赖、事务幂等记录和查询来源引用；ClickHouse 继续执行同一查询计划。迁移扩充机器授权白名单至 24 项，不给现有主体追加权限，不修改历史迁移。

公开接入 [API helper](../integrations/README.md)已允许上述指标路径，凭据仍由 owner 按实际用途签发。指标专用控制台（草稿/校验/发布/版本/运行分区）与手写 TypeScript 客户端方法已交付，生成的 Go/TypeScript 类型保持同步。控制台写操作继续携带会话 CSRF、`If-Match` 修订与幂等键；运行任务复用共享查询预算并按版本轮询结果。运行分区提供可选分组维度编辑器：事件只能从该已发布版本固定的事件中选择，属性 key 需为小写字母开头的 snake_case 标识，最多 3 个，前端在入队前复核标识与数量，边界与上文 `group_by` 契约完全一致，后端与契约无改动。

已发布的不可变指标版本可作为[保存分析](saved-analyses.md#metric-references)的数据源被固定引用：保存分析在写事务内校验 `metrics.read` 与同项目/环境，派生并持久化指标的趋势查询，同时记录指标内容摘要 pin；指标后续发布新版本不会隐式改变已发布的分析，运行前还会按 pin 二次校验。该复用当前仅支持整段指标卡片，跨指标公式参数与看板直接引用仍待交付。

实际验证见[指标服务端记录](verification/metrics-2026-11-09.md)。功能验证不改变现有百万查询矩阵中的失败记录，也不构成千万用户容量证明。
