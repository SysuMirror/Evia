# 看板软归档生命周期验证记录（2026-11-15）

## 问题（确定性缺口）

看板有完整的草稿/校验/发布/版本/运行能力，但没有任何删除或归档入口，长期堆积的看板无法从工作区列表中清理。HANDOFF 与 feature-coverage 均把「归档」列为看板缺口。

## 范围与语义决策

本轮交付**后端完整切片**（迁移+领域+存储+HTTP+契约+api-client+真实 PG 验证），控制台按钮/归档视图留下一轮。采用可逆软生命周期，决策如下：

1. **软标记而非删除**：迁移 029 给 `dashboards` 加可空 `archived_at`/`archived_by`，不可变版本、运行记录、依赖见证与审计全部保留。
2. **列表隐藏、按 ID 可读**：默认 `ListDashboards` 用 `archived_at IS NULL` 过滤（配部分索引 `dashboards_active_scope`）；`GetDashboard`/草稿/版本/运行读取路径不拦截，归档看板的历史仍可追溯。
3. **可逆双动作**：`archive` 与 `unarchive` 两个 POST 动作，重复归档返回 `DASHBOARD_ALREADY_ARCHIVED`、对活跃看板恢复返回 `DASHBOARD_NOT_ARCHIVED`，显式冲突而非静默幂等。
4. **归档后变更全部 fail-closed**：草稿保存、校验快照、发布、运行入队在归档状态下统一返回 `DASHBOARD_ARCHIVED`（409）；既有运行仍可查询/取消。
5. **审计留痕**：两个动作各写一条 `audit_entries`（`dashboard.archived`/`dashboard.unarchived`），与现有 `dashboard.created/published` 同一审计表。
6. **权限不变**：两个动作复用 `dashboards.write`，viewer 拒绝为 `PROJECT_FORBIDDEN`；不新增权限项、不放宽 scope 不可变触发器（该触发器本就允许更新非 scope 列）。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 迁移 | `029_dashboard_archive.sql` | 两可空列、部分索引、归档标记 soundness CHECK |
| 领域 | `reporting/dashboard.go`、`dashboard_service.go` | `Dashboard.ArchivedAt/ArchivedBy`；Store/Service 接口加 Archive/Unarchive |
| 存储 | `postgres/dashboard.go`、`dashboard_runs.go` | 列/扫描器、列表过滤、两个生命周期方法、四处变更守卫 |
| HTTP | `control/dashboard.go` | `POST .../archive`、`POST .../unarchive`，写权限、无请求体、200 返回 Dashboard |
| 契约 | `openapi.json` | Dashboard 加可空 `archived_at/by`；两个新路径与完整 Problem 响应 |
| 生成 | wire bundle/types、api-client schema + 两个手写方法 | codegen 同步 |

## 验证

### 单元与路由测试

- `TestDashboardHTTPRoutesContractsAndRevisions` 纳入两个新路由，钉住写角色策略与 200 调度；既有看板 HTTP 测试零修改通过；
- api-client 与生成类型 `tsc --noEmit` 通过，`archived_at?: string | null` 出现在生成类型。

### 真实 PostgreSQL 集成（隔离 schema，`-race -tags=integration`）

`TestDashboardArchiveLifecycleAndMutationGuards`（先发布再造归档，使运行授权可解析版本）：

- viewer 归档被 `PROJECT_FORBIDDEN`；归档成功返回标记，重复归档 409；
- 默认列表只返回活跃看板、归档看板按 ID/草稿仍可读且带标记；
- 草稿保存、校验、发布、运行入队四类变更全部 `DASHBOARD_ARCHIVED`；
- 未知 ID 归档 `DASHBOARD_NOT_FOUND`；对活跃看板恢复 `DASHBOARD_NOT_ARCHIVED`；恢复后标记清空、重新进入列表；
- 审计表按序出现 `dashboard.archived`、`dashboard.unarchived`。

全部既有 `TestDashboard*` 集成测试同步通过。

### 变异红绿（两轮，sha256 逐字节还原）

1. 列表过滤改为恒真 → 归档看板泄漏进默认列表，集成测试变红（`archived dashboard must leave the default listing`）；
2. 移除草稿保存的归档守卫 → 归档看板可被编辑，测试变红（`draft save must reject archived dashboard`）。

还原后基线哈希一致、全量构建通过。

### 门禁

完整 `make check` 退出码 0；codegen 幂等；命名检查无禁词。

## 明确未做（边界）

- 控制台尚无归档按钮与「已归档」列表视图，api-client 方法已就绪可供下一轮直接接线；
- 默认列表不提供 `include_archived` 参数，归档看板当前只能按 ID 直达；需要浏览归档集合的能力随控制台切片再评估；
- 看板文件夹、通用参数、比较期、直接指标版本引用仍为独立缺口。
