# 身份图与版本化身份修订

`internal/identity` 把浏览器 SDK 经 `identify()` 产生的保留事件 `sys.identity_link`（identity 同时携带 uid 与 anonymous_id）折叠为**项目+环境作用域**的身份图，产出不可变、版本化的 identity revision。设计全文与决策记录见[身份图规划](plans/identity-graph.md)；本文描述已交付的切片①（图构建只读闭环）及其边界。

## 已交付能力（切片①）

- **图模型**：二部边 `uid ↔ anonymous_id`（迁移 043 `identity_edges`），连通分量即 resolved person（`identity_persons` + `identity_person_members`）。成员只增不减；合并按既定规则「创建修订更古老者存活、平局取 resolved_person_id 字典序小者」，被吸收者退役并在 `superseded_by` 留 redirect。
- **折叠作业 `evia identity-worker`**：按 `catalog_release_activations` 枚举作用域，经 `identity_fold_state` 上的租约（条件 UPDATE + generation）串行认领；每轮扫描 ClickHouse `event_facts` 中 `sys.identity_link` 聚合对（`min/max(received_at)`、批上限 50000、水位回看 1 小时覆盖迟到/重放事实），先查 erasure tombstone 屏障（查询失败 fail-closed 中止本轮，已删除身份不建边），再由纯函数 `identity.Fold` 计算增量并单事务提交：边 upsert、新 person、退役+redirect、成员整体迁移、completed revision、水位推进。无变更的纯重叠重扫不产生新修订；折叠提交失败记 failed revision（含错误码）且不推进水位。
- **只读 API**（`control.IdentityAPI`，权限经 `authorizeOperation` 双层复核）：
  - `GET /api/v1/projects/{project_id}/identity-revisions?environment=...` — 修订倒序分页（`queries.read`；人类任意在役成员、机器显式授权），项含编号、水位、边/人/成员计数、状态与 `built_at`/`error_code`。
  - `GET .../identity-revisions/{revision}?environment=...` — 单修订详情（`queries.read`）。
  - `GET .../identity-revisions/{revision}/resolve?environment&uid|anonymous_id` — 前向身份解析（`queries.raw.read`，与引用身份字段的查询同级；viewer 角色人类被拒）。**当前仅支持解析最新 completed 修订**：历史修订的成员快照尚未持久化，旧钉版返回 `IDENTITY_REVISION_NOT_RESOLVABLE` fail-closed，不伪装可解析。
- **确定性边界**：折叠时间在领域入口统一截断到微秒（PostgreSQL `timestamptz` 精度），ClickHouse `DateTime64(9)` 的纳秒残差不会让同一对的重扫被误判为推进；`resolved_person_id` 一次性分配永不复用，重放同构而非逐字节相同。
- **观测**：`identity-worker` 经 `IdentityObserver` 上报作用域认领/跳过、扫描/屏障失败、完成/空转/失败与轮次时延，标签为固定枚举+错误码，不含业务身份值；`EVIA_METRICS_LISTEN` 暴露 `/metrics`。

## 已交付（切片②：查询集成）

`subject=resolved_person` 查询经版本化身份图解析（契约描述、discovery 广告与校验同步放开，`IDENTITY_RESOLUTION_UNAVAILABLE` 移除）：

- 入队事务内物化展开（`materializeIdentityExpansion`）：省略 `identity_revision` 取最新 completed 并把实际编号回填结果 `metadata.identity_revision`（同时移除该查询的 `identity_resolution_not_applied` 覆盖缺口）；未知修订 `IDENTITY_REVISION_NOT_FOUND`，非最新修订 `IDENTITY_REVISION_NOT_RESOLVABLE`（历史成员快照未持久化前 fail-closed）。
- 聚合类（trend UV/漏斗/分群/路径/归因/留存）经有界 `multiIf` 映射把 uid 与 anonymous_id 两列归一到 resolved id（trend 投影为此携带双身份列）；events 明细必须钉 `identity_filter.resolved_person_id`，展开为有界成员谓词（需 raw 读权限）；journey 保持单原始身份并拒绝 resolved 主体。
- 预算 fail-closed：钉人 1024 成员、全图 65536 成员，超限 `IDENTITY_EXPANSION_EXCEEDED`；持久任务携带物化映射，后续折叠不影响已入队查询。
- 回补屏障方向：折叠只读 ClickHouse，而 BarrierSink（在线写入与回补重放共用）在入库前拦截已删除身份，故回补的旧 `identity_link` 事实无法进入仓库、更无法进入图——与折叠侧屏障构成双向闭环。

## 已交付（切片③：erasure 屏障闭环）

erasure 作业 rebuilding 阶段在完成校验后调用 `RetireIdentitySubject`：删除命中主体的一切边与成员行；仅含该主体的人直接删除注册行，共享组件则整人保守退役（无 redirect）并清空成员行，剩余身份在下一轮折叠中以全新 `resolved_person_id` 重组（折叠从不把退役者视为成员所有者）。真实链路验证：删除 → 图退役 → 重放旧事实 → 不复活（屏障拦截）、幸存者换新 ID 重组；审计追加 `identity.graph_retired`。

## 已交付（切片④：控制台身份工作区）

- 「身份修订」工作区（`apps/console/src/identity`）：修订历史表（编号/状态徽标/折叠水位/边/人/成员计数/构建时间/失败错误码，签名游标分页）与身份解析表单（uid/anonymous 二选一，客户端校验匿名 UUID；解析最新完成修订，成员表含类型/标识/首见/加入修订）。权限门控：导航与页面按 `queries.read` 投影（人类任意在役成员可见），解析动作要求 `queries.raw.read`（owner/editor），无权限给出明确错误态；未知身份呈现「尚未进入身份图」错误态。
- 共享 API 客户端新增 `listIdentityRevisions`/`getIdentityRevision`/`resolveIdentityMember`；人类项目权限投影补 `queries.read`（全员）与 `queries.raw.read`（owner/editor），机器权限不变。
- 验证：模型单测 6 例（表单校验/状态词表/最新完成修订选择/解析摘要/格式化退化）；全栈浏览器 spec（`EVIA_E2E_FULL_STACK` 门控）：真实 SDK `autoIdentityLink` 双设备 → identity-worker 折叠 → 工作区列表/跨设备解析/404 错误态全链通过；桌面/解析结果/移动端真实截图（验证记录）。

## 尚未交付（按规划切片推进）

- 历史修订成员快照（或 delta 重放）：resolve 与查询钉版暂仅最新 completed 修订。
- 保留策略清理（决策：90 天或 1000 个取大）尚未实现，v1 不删除修订。

## 运行

```bash
python3 tooling/verification/browser_runtime.py run <runtime.env> -- ./bin/evia identity-worker
```

与 ingest/query worker 相同的配置面（PostgreSQL + ClickHouse）；无需 Kafka/Redis。轮询 60s、租约 5 分钟、扫描批 50000 对为工程缺省，多副本经租约分区工作。
