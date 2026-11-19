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

## 尚未交付（按规划切片推进）

- 切片②：查询侧开放 `subject=resolved_person` 与 `identity_revision`（计划期有界展开，成员上限 1024，`IDENTITY_EXPANSION_EXCEEDED` fail-closed；journey 继续拒绝）。在此之前 `analysis` 校验保持 `IDENTITY_RESOLUTION_UNAVAILABLE` 原样。
- 切片③：erasure rebuilding 联动退役与回补双向屏障闭环。
- 切片④：控制台身份修订工作区。
- 历史修订成员快照（或 delta 重放）：resolve 的旧钉版支持依赖它，当前 fail-closed。
- 保留策略清理（决策：90 天或 1000 个取大）尚未实现，v1 不删除修订。

## 运行

```bash
python3 tooling/verification/browser_runtime.py run <runtime.env> -- ./bin/evia identity-worker
```

与 ingest/query worker 相同的配置面（PostgreSQL + ClickHouse）；无需 Kafka/Redis。轮询 60s、租约 5 分钟、扫描批 50000 对为工程缺省，多副本经租约分区工作。
