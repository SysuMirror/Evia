# 看板同环境复制验证记录（2026-11-17）

## 问题（确定性缺口）

长期方案要求「复制创建新 ID，保留明确的版本依赖，跨环境复制必须重新绑定并校验，不能沿用原环境权限或发布资格」。此前看板只有创建、移动、归档，没有复制能力，作者想基于既有看板派生新版本只能手工重建。本切片交付**同项目同环境**复制的后端事务、契约、客户端与控制台闭环；跨环境重绑定因需要独立的字段重映射语义，明确留作后续切片，且契约不提供任何覆盖源环境的请求字段（fail-closed by omission）。

## 范围与语义决策

1. **独立新资源**：副本获得全新 `dashboard_id` 与全新组件 `widget_id`；固定的 analysis/metric 不可变引用、布局、阅读顺序、参数声明与组件绑定原样保留。参数按 `parameter_id` 引用，组件 ID 重生成不破坏绑定。
2. **不携带生命周期状态**：副本从 `draft_revision=1`、`published_version=NULL` 起步，不复制 `dashboard_versions`、`dashboard_runs`、幂等历史与归档状态（副本恒为活跃）。源看板的发布、运行与归档状态完全不变。
3. **同环境限定**：复制在源看板所属 environment 内完成；目标 `folder_id` 必须属于同一 project/environment，否则 `DASHBOARD_FOLDER_NOT_FOUND`。无跨环境入参，跨环境复制留待显式重绑定切片。
4. **幂等与审计**：操作名 `copy:{source_id}` 写入既有 `dashboard_idempotency`，同键同输入重放返回同一副本，改输入同键 `IDEMPOTENCY_CONFLICT`；新资源写 `dashboard.copied` 审计。
5. **授权**：源资源经 `authorizeReportingResource(..., "dashboards.write")` 校验存在性与写权限；归档看板允许复制（读路径可用且不动源状态），副本是活跃新草稿。
6. **无新迁移**：复用 `dashboards`、`dashboard_idempotency`、`audit_entries` 与 035 的 `folder_id uuid` 外键，形状门控（迁移 037）自动覆盖副本定义。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 领域 | `reporting/dashboard.go`、`dashboard_policy.go`、`dashboard_service.go` | `DashboardCopy`、双层接口方法、`CloneDashboardDefinition` 纯克隆助手、服务契约/文件夹校验 |
| 存储 | `postgres/dashboard_copy.go` | 单事务授权→读源→文件夹断言→克隆→幂等→插入→审计→读回 |
| 集成测试 | `postgres/dashboard_copy_integration_test.go` | 3 个真实 PG 测试覆盖独立副本、文件夹/参数保留、未知源与归档源 |
| 契约 | `openapi.json` | `DashboardCopy` schema 与 `POST dashboards/{id}/copy` 路由（幂等头+CSRF+201） |
| 控制层 | `transport/control/dashboard.go`、`dashboard_test.go` | copy handler、假服务、路由表与严格输入/CSRF 边界测试 |
| 客户端 | `api-client/index.ts`、`index.test.ts` | `copyDashboard` 方法、类型导出与专用测试（42 项） |
| 控制台 | `copy-dashboard.tsx`、`list.tsx`、`use-draft.ts`、`use-controller.tsx`、`dashboards.tsx` | 复制弹窗、活跃/归档双入口、`openExisting` 直达新草稿 |

## 验证

### 真实 PostgreSQL 集成（`-tags=integration`）

- `TestDashboardCopyCreatesIndependentDraft`：副本 ID 不同于源、r1/未发布、组件 ID 全部重生成而固定引用保留；`dashboard_versions`/`dashboard_runs` 对副本为 0 行；源发布版本不变；`dashboard.copied` 审计恰一条；同键重放返回同副本；改标题同键 `IDEMPOTENCY_CONFLICT`。
- `TestDashboardCopyRetainsParametersAndTargetsFolder`：副本落入指定文件夹，参数声明与绑定（含绑定参数标识）保留且组件 ID 更新；未知文件夹 `DASHBOARD_FOLDER_NOT_FOUND`。
- `TestDashboardCopyMissingSourceAndArchivedSource`：未知源 `DASHBOARD_NOT_FOUND`；归档源可复制，源 `archived_at` 保持非空而副本为空（活跃）。

### 领域与边界单测

- `TestCloneDashboardDefinitionFreshIdentityAndRetainedPins`：新组件 ID、固定引用/参数/绑定保留、克隆与源的切片存储互不污染、ID 数量不匹配拒绝。
- 控制层 `TestDashboardHTTP*`：合法复制 201 且计写权限；缺幂等键、空标题、未知 body 字段、缺 CSRF 全部在边界 400/403，不到达服务。
- api-client 新增复制路由测试：URL 编码、POST、CSRF/幂等头、请求体与中止信号；客户端全套 42 项通过。

### 变异红绿（sha256 逐字节还原）

1. 存储层把新组件 ID 退化为沿用源 ID（基线 `10e946c4…`）→ 集成测试报「widget ID must be regenerated」变红，Edit 还原后 `sha256sum -c` OK 复绿。
2. 控制台复制弹窗把 folderID 强转为 number（基线 `9a82aa0d…`）→ tsc TS2345/TS2352 变红，还原哈希一致后复绿。

### 门禁

- `make check PYTHON=…/venv/bin/python` 退出码 0：命名/契约/边界/监控/后端测试/Web（typecheck、prettier、build）全绿；`verify_contracts.py` 通过、codegen 幂等（`generated-check` 无 diff）、api-client 42 项测试通过。
- 全量 PG 集成包 `go test -count=1 -tags=integration -skip TestFailoverBaselineWrite ./internal/storage/postgres/...` 一次通过（92.2s，0 FAIL）。`TestFailoverBaselineWrite` 属既有多主机环境门控，与本切片无关。

## 明确未做（边界）

- **跨环境复制未实现**：固定版本按环境隔离，跨环境需要显式重绑定与重新校验；当前契约没有 target environment 字段，服务端也无从接受。
- 复制不复制发布历史、运行结果、归档状态与幂等记录，这是刻意的资源隔离而非遗漏。
- 控制台无组件级单测设施，交互以 tsc 类型守卫（含变异红绿）、构建与后端真实 PG 集成测试为证据。
