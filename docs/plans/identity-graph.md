# 身份关联与可版本化身份图：设计备忘录

> **状态：切片①（图构建只读闭环）已交付，其余切片规划中。** 已交付部分以 [docs/identity.md](../identity.md) 为准；本文其余内容仍是设计提案。切片①落地时的一项修订：resolve API 当前仅支持最新 completed 修订（历史成员快照未持久化，旧钉版 `IDENTITY_REVISION_NOT_RESOLVABLE` fail-closed），§7 草案中的任意修订解析推迟到快照能力落地。

## 1. 目标与非目标

### 目标（v1）

- 把已可查询的 `sys.identity_link` 事实（浏览器 SDK `autoIdentityLink` 产生，`identity` 同时携带 `uid` 与 `anonymous_id`，经 Kafka→ClickHouse 全链路落库）折叠为**项目+环境作用域**的身份图。
- 图模型为二部边 `uid ↔ anonymous_id`，连通分量即 resolved person；成员集合随边单调增长（append-only），v1 不存在删除边以外的图编辑能力。
- 每次折叠产出**不可变、版本化的 identity revision**（快照 + 水位），与查询任务/结果快照同构：revision 编号单调递增，完成后的行不可改写。
- 查询侧开放预留的 `subject=resolved_person` 与 `identity_revision`（当前 `backend/internal/analysis/validation.go:62` 与 `:107` 以 `IDENTITY_RESOLUTION_UNAVAILABLE` fail-closed），在计划期把 resolved 身份展开为有界原始身份集合，并在结果 `metadata.identity_revision` 回填实际使用的快照（契约字段已存在于 `contracts/query/query-result.schema.json`）。
- 与按身份删除（erasure）的 tombstone 屏障闭环：已删除身份不得经由身份图或回补复活。

### 非目标（v1 明确不做）

- 不做跨项目、跨环境合并；一个 (project_id, environment) 一张图。
- 不做 uid↔uid 直接关联、服务端来源事件折叠、图的人工编辑/撤销（erasure 删除除外）。
- 不做 `resolved_person` 的 journey（行为链）：journey 语义钉死「单个原始身份」，展开成多身份会改变其因果叙述，v1 继续拒绝。
- 不做身份图的历史时点穿越查询（「上周这个人是谁」）；revision 快照钉版已提供该能力的构建块，但 v1 不承诺跨 revision 的 diff/演化的查询语法。
- 不做 SDK 侧变更：`sys.identity_link` 的产生规则（`identify()` 时 uid+anonymous 快照、不采凭据）不变。

## 2. 数据模型（PostgreSQL 为元数据真源）

新迁移从 **043** 起编号（当前最新为 `042_export_format_parquet.sql`；以 `backend/internal/storage/postgres/migrations/` 实际最新为准，落地时顺延）。建议拆两张迁移：

- `043_identity_graph.sql`：边、人、成员、revision 四张表 + 折叠水位单例行。
- `044_identity_rebuild_grant.sql`（仅当采纳 §7 的机器触发授权时）：封闭机器授权 `identity.rebuild`，沿用迁移 016（`data.erasure`）的先例。

| 表 | 关键列 | 说明 |
| --- | --- | --- |
| `identity_edges` | (project_id, environment, uid, anonymous_id) PK；`first_seen_at`、`last_seen_at`、`first_revision`、`last_revision` | 从 `sys.identity_link` 事实折叠的边。append-only：同一对身份重复出现只更新 `last_seen_at`/`last_revision`，不新增行。**不含来源 event_id**——边是身份对的聚合事实，不是事件副本；重扫天然幂等。 |
| `identity_persons` | (project_id, environment, resolved_person_id) PK；`state`（active/retired）；`superseded_by`（retired 时指向合并后的存活者）；`created_at`、`create_revision` | resolved person 的持久注册表。`resolved_person_id` 为 UUID，**一次性分配、永不复用**。合并时被吸收者置 retired 并留 redirect 链，查询解析链深度有界（≤16，超限 fail-closed 重建；2026-11-20 决策）。 |
| `identity_person_members` | (project_id, environment, resolved_person_id, member_kind, member_value) PK；`first_seen_at`、`joined_revision` | 成员归属。`member_kind` 闭集 `uid`/`anonymous_id`。一人多成员（≥1 uid 且 ≥1 anonymous 为常态）。 |
| `identity_revisions` | (project_id, environment, revision) PK，revision 为项目内 bigserial；`watermark_received_at`、`edge_count`、`person_count`、`member_count`、`state`（building/completed/failed）、`built_at`、`error_code` | 不可变快照目录。completed 行只读；failed 行保留失败码供诊断，下一次成功折叠产生新 revision，不覆盖。 |
| `identity_fold_state` | (project_id, environment) PK；`last_completed_revision`、`scan_watermark`、租约列（generation/owner/until） | 折叠水位与 worker 租约，形状对齐 `erasure_jobs`/`export_jobs` 的 SKIP LOCKED 租约模式。 |

### 确定性与稳定 representative 规则

纯「成员集合的函数」（如字典序最小成员）不足以给出稳定 ID：新成员加入可能翻转代表元，导致同一人在不同 revision 间换 ID。因此采用**持久分配 + 确定性合并规则**：

1. 首次出现的连通分量分配新 `resolved_person_id`（安全 UUID）。
2. 后续折叠使两个分量连通时，**创建 revision 更小者存活**（即更古老的分量延续其 ID）；创建 revision 相同时取 `resolved_person_id` 字典序小者。被吸收分量整体 retired 并逐成员挂 redirect。
3. 规则是 (边集合, 分配历史) 的确定性函数：同一事实序列在任何重放/重扫下产生同构结果；redirect 链保证钉住旧 ID 的历史查询语义可解释。
4. 「古老者存活」的业务含义：账号维度的 uid 通常先于设备维度形成分量，倾向让账号分量吸收设备分量；这是可解释的缺省，不是人级正确性断言。

### 水位与重扫

- 折叠扫描 ClickHouse 中 `event_key='sys.identity_link'` 且 `received_at > scan_watermark - overlap` 的事实（建议 overlap = 1 小时），去重视图 `event_facts` 已按 (project, environment, source, event_id) 去重，边 upsert 又以身份对为键，因此重扫幂等。
- `received_at` 是采集端接收时间：重放事件获得新 `received_at` 但身份对不变，overlap + 幂等 upsert 覆盖迟到事实；周期性（建议每日）全量重建作为漂移自愈，产物仍是「新 revision」而非改写历史。
- 水位只在 revision 事务提交时推进，崩溃恢复后从 `last_completed_revision` 的水位重扫。

## 3. 冲突与不变量

- **单调合并、无写冲突**：边只增不减；分量只合并不分裂——除 erasure 触发的保守退役（§5）。折叠 worker 是唯一写者，经租约串行化，不存在并发图写。
- **跨环境隔离**：所有表以 (project_id, environment) 为首键；查询展开永不跨环境取成员。机器凭据本身单项目单环境，与该边界同构。
- **erasure 屏障**（§5 详述）：折叠 worker 扫描前必须查询 tombstone 屏障（与写入侧 `BarrierSink` 相同的谓词），对已删除身份的 `identity_link` 事实**跳过且不建边**；屏障查询失败 fail-closed 中止本轮折叠，不得降级为「照常建边」。
- **权限映射**：沿用现有词汇，不新造读权限——
  - revision 列表/详情（仅元数据：编号、水位、计数、状态）→ `queries.read`；
  - person 成员查找（暴露原始 uid/anonymous 标识）→ `queries.raw.read`（与「引用身份字段的查询」同级别；`docs/machine-access.md` 的分类器原则同样适用：不能靠换资源类型绕过）；
  - 手动触发重建 → owner 人类会话（CSRF + Idempotency-Key，If-Match 不适用——无修订语义的资源）；机器触发为可选决策（§7-Q4）。
- **审计**：折叠成功/失败、手动触发、erasure 联动退役均写 `project_audit`，操作码进闭集。

## 4. 处理作业：`identity-worker`

- 新增 `evia identity-worker` 子命令，进程形状对齐 `erasure-worker`/`retention-worker`：有界轮询间隔（建议 60s，可配）、SIGTERM 优雅排空、`EVIA_METRICS_LISTEN` 管理监听、`IdentityObserver` 领域端口上报折叠轮次/作用域/结果/边数/时延（复用 erasure/query 的 Observer 模式，role 标签 `identity-worker`）。
- 每轮：按 (project, environment) 逐作用域 `SELECT ... FOR UPDATE SKIP LOCKED` 认领租约（generation 计数，建议 5 分钟）→ 查 erasure 屏障 → 扫描增量事实 → 单事务内 upsert 边、合并分量、写新 revision、推进水位 → 释放租约。
- 失败重试：租约过期可被重认领；连续失败沿用三尝试预算后置 failed revision（错误码入表），不阻塞其他作用域。事务边界内完成全部写入，不允许「先写边再异步补 revision」。
- 幂等：作用域无新事实（水位后零行）不产生新 revision——revision 只在有实际折叠时递增，避免空转刷号。

## 5. erasure 联动（删除屏障）

erasure 作业已有 `hiding→purging→rebuilding→completed` 状态机（`internal/erasure`），`rebuilding` 阶段是派生状态重建挂点：

1. erasure `rebuilding` 阶段追加一步：删除该 (project, environment, subject) 命中的 `identity_edges`、`identity_person_members` 行；受影响的 person 若变空则直接删除注册行，否则**整人保守退役**（retired，不试图分裂分量——分裂需要删除边以外的图推断，超出 append-only 语义）。退役人的 `superseded_by` 为空：被删除污染的分量不 redirect 到新 ID，剩余成员在下一轮折叠中以新 `resolved_person_id` 重组。
2. 屏障两个方向都要堵：折叠扫描跳过已删除身份（§3）；归档回补（backfill）重放的 `identity_link` 事实同样先过 tombstone 才能进折叠——回补 worker 已与 BarrierSink 共识「重复回补查询安全」，身份图必须显式复用同一屏障查询而不是重写一份。
3. 验收断言（进 slice 3）：删除 → 图退役 → 回补同一身份的旧事实 → 图不复活该成员；「已删除数据不因恢复复活」的既有不变量扩展到身份图。
4. 边界（如实记录）：与被删除身份共享设备的其他人会获得新 resolved_person_id（旧 ID 已退役），这是隐私保守取向的代价。

## 6. 查询集成（`subject=resolved_person`）

### 展开语义

- 请求 `subject=resolved_person` 时**必须**同时可解析 revision：显式 `identity_revision` 钉住指定快照；省略则解析该 (project, environment) 最新 completed revision，并把实际使用的编号回填到 `metadata.identity_revision`（契约描述已如此预留）。指定 revision 不存在/未完成 → `IDENTITY_REVISION_NOT_FOUND`（新错误码，fail-closed）。
- 计划期把 resolved_person 展开为原始身份集合，作用于主体列选择：
  - `unique`/计数主体：对成员集合构造有界映射表达式（`multiIf(uid IN (...), p, anonymous_id IN (...), p, ...)` 风格，参数绑定），distinct 按 resolved id 计——同一人多个 uid/anonymous 只计一次；
  - filter/journey 类主体列：trend/events 的身份过滤接受 resolved id 并展开为成员谓词（OR 连接，参数绑定）；
  - funnel/segment/path/attribution/retention 的按主体拉取：主体列从单列（uid 或 anonymous_id）变为「uid 列 ∪ anonymous_id 列」经映射表达式归一到 resolved id。
- **预算**：新独立预算 `identity_expansion_members`（上限 1024 成员/人，2026-11-20 决策从宽；`docs/analysis.md` 预算表追加一行）；超限返回 `IDENTITY_EXPANSION_EXCEEDED`（新错误码），不静默截断成员。展开后的谓词参数不计入 filter 节点预算（它们不是用户过滤树），但计入查询整体参数绑定规模护栏。
- **journey/events 明细**：`events`（raw 行）允许 resolved_person（成员谓词过滤，需 `queries.raw.read`）；`journey` v1 拒绝（非目标），错误信息指向「用原始身份查 journey 或用 events 拉成员明细」。
- revision 钉版一致性：展开集合在该 revision 的成员表快照上读取；查询执行期间新 revision 落地不影响已入队查询（成员集合在计划期物化进任务）。

### 放开顺序

`IDENTITY_RESOLUTION_UNAVAILABLE` 的移除必须与查询集成同片落地：slice 2 之前该拒绝码及其契约描述保持原样，不允许出现「revision 可建但查询仍拒绝」之外的中间态广告。

## 7. API / 契约草案

前缀 `P = /api/v1/projects/{project_id}`；响应用 `{items,next_cursor}` / 资源直返 / 错误体 `{code,message,request_id,details?}`，与现有治理、指标接口同构。契约改动走 `contracts/` → codegen → 生成物提交，不手改生成文件。

| 操作 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| revision 列表 | `GET P/identity-revisions?environment=...` | `queries.read` | 倒序分页，项含编号、水位、边/人/成员计数、状态、built_at。 |
| revision 详情 | `GET P/identity-revisions/{revision}` | `queries.read` | 同上单资源；含 failed 的 error_code。 |
| 身份解析查找 | `GET P/identity-revisions/{revision}/resolve?uid=...` 或 `?anonymous_id=...` | `queries.raw.read` | 返回 resolved_person_id、全部成员、redirect 已解析；不存在返回 404 资源体。 |
| 触发重建 | `POST P/identity-rebuilds`（Idempotency-Key 必填） | owner 人类（机器 `identity.rebuild` 为决策项） | 入队一次全量重建作业，复用 identity-worker 执行；重复幂等键返回原作业。 |

契约同步项：`query-request.schema.json` 的 `subject`/`identity_revision` 描述改写（去掉「currently fails closed」）、结果 `metadata.identity_revision` 描述确认、新错误码进错误词表、`docs/analysis.md` 语义与预算章节更新。

## 8. 分片交付计划（每片：后端 + 真实依赖验证 + 文档）

| 切片 | 内容 | 依赖 | 独立验收条件 |
| --- | --- | --- | --- |
| 1. 图构建只读闭环 ✅（2026-11-20 交付，见 [docs/identity.md](../identity.md)；审计操作码待 erasure 联动切片一并闭集化） | 迁移 043、`internal/identity` 领域 + PG 存储、`identity-worker` 折叠、revision/resolve API + 契约 codegen、权限接线、审计、Prometheus 指标 | 无 | 真实 ClickHouse 合成 `sys.identity_link`（含重放与迟到事实）→ 真实 PG revision 构建：单调合并、redirect、水位幂等、租约重认领、failed revision 语义、屏障查询失败 fail-closed；HTTP 全链（401/403/404/分页/幂等）。此片不触碰查询验证器，`IDENTITY_RESOLUTION_UNAVAILABLE` 原样。 |
| 2. 查询集成 | `subject=resolved_person` + `identity_revision` 计划期展开、成员预算与两个新错误码、`metadata.identity_revision` 回填、journey 拒绝边界 | 1 | 先红后绿：现有拒绝用例改造；真实 CH+PG 合并身份的 trend/unique/funnel/segment 对照（手工展开的等价 SQL 结果一致）；超成员预算 fail-closed；同 revision 钉版不受并发新 revision 影响；race 全量通过。 |
| 3. erasure 屏障闭环 | erasure rebuilding 联动退役、折叠/回补双向屏障、恢复不复活 | 1（与 2 可并行，但都改 `internal/identity`，需错峰或分文件所有权） | 真实链路：删除 → 图退役/重组 → 回补旧事实 → 不复活；屏障失败中止折叠；审计完整。 |
| 4. 控制台与文档 | 身份修订工作区（revision 表、resolve 查找、状态徽标、失败诊断）、导航门控 | 1（API），2（查询侧展示） | Playwright 全栈：列表/查找/错误态/权限拒绝态；桌面+移动截图；`docs/` 全套同步。 |

每片完成后更新 PROGRESS、覆盖清单（「未实现」→ 对应切片边界的「部分」）、`docs/identity.md`（新建模块文档）。

## 9. 风险与开放问题（需用户决策）

### 决策记录（2026-11-20，用户授权自主拍板：限制从宽、确定性优先）

| # | 问题 | 决策 | 理由 |
| --- | --- | --- | --- |
| Q1 | 成员展开上限取值 | **1024/人**（`IDENTITY_EXPANSION_EXCEEDED` 超限 fail-closed） | 用户明确要求限制从宽；1024 仍保有界（映射表达式与参数绑定规模可控），比建议缺省 256 放开一档，覆盖大账户多设备共用场景 |
| Q2 | representative 合并规则 | 「创建 revision 更古老者存活，平局取 resolved_person_id 字典序小者」 | 确定性与稳定性优先：同事实序列任何重放同构，redirect 链保证钉版历史可解释；不引入成员类型偏好 |
| Q3 | 省略 `identity_revision` 时取最新 | 省略=最新 completed revision，`metadata.identity_revision` 回填实际编号 | 契约已允许省略；最新 completed 是可解释缺省，陈旧性由 revision 元数据（水位）显式暴露而非隐藏 |
| Q4 | 机器触发重建授权 | v1 仅 owner 人类（CSRF + Idempotency-Key）；迁移 044 与 `identity.rebuild` 机器授权留待自动化运维需求出现时补 | 控制封闭权限词汇增长；重建属运维破坏面较小的动作但暂无机器自动化场景 |
| Q5 | revision 保留策略 | **保留最近 90 天或 1000 个 completed（取大）**，更早归档删除 | 限制从宽；切片①不实现清理（无删除路径），决策值先入文档，随保留策略切片落地时需要配套「钉住已删除 revision」的失效错误码 |
| Q6 | 折叠事实来源 | v1 仅 `sys.identity_link`（browser 来源、经 `identify()` 显式声明） | 语义纯度优先：服务端事件的 uid+anonymous 不是显式身份声明，混入会弱化「用户确认过的关联」语义；后续版本可评估 |

同批拍板的相关边界（同样从宽）：redirect 解析链深度上限 **16**（原建议 8）；折叠扫描 overlap 保持 1 小时；`identity-worker` 轮询 60s、租约 5 分钟（运维参数非用户限制，按工程缺省）。

### 原开放问题清单（决策前存档）

| # | 问题 | 建议缺省 | 风险 |
| --- | --- | --- | --- |
| Q1 | 成员展开上限取值 | 256/人（映射表达式规模与 ClickHouse 参数绑定上限的折中） | 过小伤大账户共用设备场景，过大伤计划期内存与 SQL 规模；需要真实分布数据校准 |
| Q2 | representative 合并规则 | 「创建 revision 更古老者存活」 | 更贴业务的规则（如 uid 成员优先）可能更符合论坛场景直觉，但引入成员类型偏好，规则复杂度上升 |
| Q3 | 省略 `identity_revision` 时取最新 | 省略=最新 completed，metadata 回填 | 长折叠延迟下「最新」可能是陈旧快照；另一选项是 v1 强制显式钉版（更保守，但契约已允许省略） |
| Q4 | 机器触发重建授权 | v1 仅 owner 人类；`identity.rebuild` 机器授权留到有自动化运维需求时随迁移 044 补 | 新增封闭权限词汇的维护成本 |
| Q5 | revision 保留策略 | 保留最近 30 天或 100 个（取大），更早的 completed 行归档删除 | 钉住已删除 revision 的历史查询/保存分析运行会失效，需要与保存分析「事件依赖固定」语义对齐的失效错误码 |
| Q6 | 折叠事实来源是否扩展到 server 来源凭据事件的 identity 字段 | v1 仅 `sys.identity_link`（browser） | 服务端采集的事件同样携带 uid+anonymous，天然是边证据；纳入可提高覆盖率但混入未经 `identify()` 显式声明的关联，语义变弱 |

其他如实记录的风险：identity 图的查询容量未在本设计内验证（slice 2 的对照测试不构成容量声明）；`multiIf` 映射表达式的 ClickHouse 执行成本需在 slice 2 用真实规模数据补测；「同一人多设备」的跨环境合并、身份图时点穿越、撤销/重合并编辑均为后续版本课题。

## 10. 与现有文档的关系

- [analysis.md](../analysis.md)：「Resolved-person queries and identity revisions reject explicitly until a real versioned identity graph exists」——slice 2 落地时改写该段并补预算行。
- [browser-sdk.md](../browser-sdk.md)：「identity graph processing remains separate work」——slice 1 落地后更新为指向本文的已实现边界。
- [feature-coverage.md](../feature-coverage.md)：第 22 行（未实现）按切片进度推进，不提前改「部分」。
- 本文档自身在 slice 1 落地后逐节标注「已交付」，并保留失败/边界记录，方式对齐 [property-read-layout](property-read-layout.md) 的分节核销风格。
