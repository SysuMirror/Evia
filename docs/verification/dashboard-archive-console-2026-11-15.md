# 看板归档列表视图与控制台接线验证记录（2026-11-15）

## 问题（确定性缺口）

上一切片交付了看板软归档后端（迁移 029 + archive/unarchive 生命周期），但存在两个确定性缺口：

1. 后端 `ListDashboards` 硬编码 `archived_at IS NULL`，没有任何参数可以浏览已归档集合，归档后除按 ID 直达外无法在接口层枚举恢复对象；
2. 控制台没有归档入口与归档视图，api-client 的 `archiveDashboard`/`unarchiveDashboard` 方法无调用方，能力对用户不可达。

## 范围与语义决策

本轮交付**后端列表视图参数 + 控制台全链路**，默认行为保持逐字节不变：

1. **闭集 `view` 参数而非自由文本**：`GET P/dashboards` 新增可选 `view=active|archived`，缺省与 `active` 等价（活跃列表）；未知值返回 400 `INVALID_ARCHIVE_VIEW`，重复参数沿用共享白名单拒绝为 `INVALID_QUERY`。共享助手 `savedQuery` 重构为 `savedQueryWith` + 两个语义化包装（`savedQuery`/`savedArchiveQuery`），其余 40+ 调用点签名零改动。
2. **谓词由校验后的布尔量选择**：PG 层 `archivedOnly bool` 仅在两个常量谓词（`archived_at IS NULL` / `IS NOT NULL`）间二选一，请求文本永不进入 SQL。
3. **游标与视图绑定**：游标 scope 追加视图段（`dashboards:{actor}:{project}:{env}:{active|archived}`），跨视图复用游标一律 `INVALID_CURSOR`，杜绝用归档游标翻活跃页或反向泄漏。
4. **控制台双视图**：列表页提供「活跃看板/已归档」切换（`aria-pressed`），切换即重置分页与查询键；归档为 danger 按钮 + 确认 Modal（明确告知编辑/发布/运行关闭、可恢复），归档视图显示归档时间、禁用打开草稿、仅提供恢复操作；viewer 看不到可用写按钮（禁用态）。
5. **变更后即时一致**：归档/恢复成功后失效整个 `dashboards` 查询前缀，两个视图同时刷新，行立即出现在新流；成功/失败分别走 toast 与页内 `CatalogError`。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| HTTP 助手 | `control/reporting_saved.go` | `savedQueryWith` 重构、`savedArchiveQuery` 包装、`view` 白名单与闭集校验 |
| 领域 | `reporting/dashboard.go`、`dashboard_service.go` | 两个接口的 `ListDashboards` 增加 `archivedOnly bool` |
| 存储 | `postgres/dashboard.go` | 布尔选择常量谓词，无请求文本拼接 |
| HTTP | `control/dashboard.go` | 列表处理器接入 `view`，游标 scope 绑定视图 |
| 契约 | `openapi.json` | `listDashboards` 增加 `view` 查询参数（枚举、默认 active、中文说明） |
| 生成 | `schema.gen.ts` | 操作类型出现 `view?: "active" | "archived"`（wire bundle 只编码 schema 组件，路径参数不落地，故无 Go 产物差异） |
| api-client | `packages/api-client/src/index.ts` | `CatalogPage.view`、`catalogQuery` 输出、新增 `DashboardPage` 类型并收窄 `listDashboards` 入参 |
| 控制台 | `dashboards/use-history.ts`、`use-controller.tsx`、`list.tsx`、`dashboards.tsx`、`styles.css` | 视图状态/查询键/失效刷新、归档恢复动作、确认 Modal、双视图表格与样式 |

## 验证

### 路由单元测试（`internal/transport/control`）

新增 `TestDashboardHTTPArchiveViewSelector`：

- 缺省/`view=active` 转发 `archivedOnly=false`，`view=archived` 转发 `true`；
- `view=bogus` → 400 `INVALID_ARCHIVE_VIEW` 且不触达服务；重复 `view` 与条目路由携带 `view` → 400 `INVALID_QUERY`（严格白名单不扩散）；
- 归档流游标只能在 `...:archived` scope 解码，回放到活跃列表 → 400 `INVALID_CURSOR` 且不触达服务；
- 既有 `TestDashboardHTTPCursorsBoundScopeAndPosition` 的 scope 期望同步为 `...:active`。

### 真实 PostgreSQL 集成（隔离 schema，`-race -tags=integration`）

`TestDashboardArchiveLifecycleAndMutationGuards` 增补断言：归档后 `archivedOnly=true` 列表恰返回该看板且带标记、活跃列表恰返回另一看板；恢复后归档流为空、活跃流回到 2 行。全部既有看板/查询机器集成测试仅追加布尔实参，语义不变。

### 变异红绿（三轮，sha256/逐字节还原）

1. 移除 `view` 闭集拒绝分支 → `view=bogus` 被当活跃流放行返回 200，新测试如期变红；
2. 反转 SQL 谓词选择 → 默认列表泄漏归档看板，集成测试如期变红（`archived dashboard must leave the default listing`）；
3. 移除 `catalogQuery` 的 `view` 输出 → 客户端归档流 URL 丢失视图参数，新增 api-client 用例如期变红（其余 35 用例保持通过）。

三轮均还原到基线后转绿（前两轮以 sha256 校验，客户端轮以 `git diff` 零差异确认）。

### 前端与门禁

- `pnpm typecheck`（全工作区 tsc）零错误；`pnpm test:client` 36/36 通过（新增专用用例钉死 `view` 查询串编码顺序、archive/unarchive 的 POST/CSRF/无请求体/无幂等键与信号转发）；`pnpm build` 控制台构建成功；prettier 全量格式化通过；
- 完整 `make check` 退出码 0（32 个 Go 包 ok、0 FAIL）；codegen 幂等；命名检查无禁词。

## 明确未做（边界）

- 控制台无组件级单测设施，前端以 tsc 严格类型、生产构建与既有控制器模式（`perform` 单变更槽、react-query 查询键）保证；全栈浏览器 e2e 属凭证门控，不在默认 `make check`；
- 归档视图不提供打开草稿入口（归档态所有写路径 409），仅支持恢复；按 ID 直达的读能力保持不变；
- 看板文件夹、通用参数、比较期、看板直接指标版本引用仍为独立缺口。
