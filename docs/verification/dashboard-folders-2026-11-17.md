# 看板文件夹层级验证记录（2026-11-17）

## 问题（确定性缺口）

看板只有平铺列表，项目内看板数量增长后没有任何组织手段：长期方案（[自定义看板方案](../plans/custom-dashboards.md)）设计了 `dashboard_folder` 实体（项目/环境范围内父目录、禁环、有限深度、继承项目权限），但库表、API 与契约均不存在。本切片交付该实体的后端、契约、真实数据库与手写客户端能力（控制台 UI 留后续切片）。

## 范围与语义决策（已拍板）

1. **深度上限 3**：根文件夹 depth=1，最深允许 depth=3；在 depth=3 节点下再建子级返回 `DASHBOARD_FOLDER_TOO_DEEP`。
2. **仅空文件夹可硬删除**：仍含子文件夹或看板时返回 409 `DASHBOARD_FOLDER_NOT_EMPTY`；不做子项上浮或级联，杜绝隐式批量变更。数据库双外键 `ON DELETE RESTRICT` 是竞态兜底。
3. **仅条件硬删除，不做软归档**：文件夹是没有不可变版本见证的纯组织元数据，不引入 `archived_at`，表面积最小。
4. **层级位置创建后不可变**：`parent_id`/`depth` 由 scope 触发器保护，PATCH 仅允许改名（带 `If-Match` 修订）；重组路径是「新建文件夹 + 移动看板 + 删除空旧文件夹」。
5. **看板归属为全量替换字段**：`DashboardCreate`/`DashboardUpdate` 的可选 `folder_id`，省略或空表示根级；列表支持 `folder_id=<uuid>` 或 `folder_id=root` 过滤，游标与该范围绑定。
6. **权限不新增**：文件夹读写复用 `dashboards.read`/`dashboards.write`，机器主体与会话走同一授权；同项目同环境强制，跨环境父级按 not-found 处理不泄漏存在性。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 迁移 | `035_dashboard_folders.sql` | `dashboard_folders` 表、深度 1–3 CHECK、自引用 MATCH SIMPLE 外键、scope 不可变触发器；`dashboards.folder_id` + RESTRICT 外键与索引 |
| 领域 | `reporting/dashboard_folder.go`、`dashboard.go` | 文件夹类型/接口、创建/更新/范围规范化；看板读写模型与 Create/Update 增加 `folder_id`，接口签名扩展 |
| 服务 | `dashboard_folder_service.go`、`dashboard_service.go` | 契约校验、环境/UUID/分页门禁、文件夹作用域透传 |
| 存储 | `postgres/dashboard_folders.go`、`dashboard.go`、`query.go` | 五方法事务实现、幂等复用 `dashboard_idempotency`、审计三类动作、深度/空删/同域校验、列表谓词与资源授权分支 |
| HTTP | `control/dashboard_folders.go`、`dashboard.go`、`access.go`、`reporting_saved.go`、`cmd/evia/runtime.go` | 五个新路由、ETag 修订、204 删除、白名单增加 `folder_id`、服务装配 |
| 契约 | `openapi.json` | 4 个新 schema、2 条新路径、看板 schema 与列表参数扩展；codegen 重生 Go wire（182 类型）与 TS |
| 客户端 | `packages/api-client/src/index.ts` | 5 个语义化方法、`CatalogPage.folderId` 与查询串编码 |
| 测试 | `dashboard_folder_test.go`、`dashboard_folders_integration_test.go`、`dashboard_folders_test.go` 及既有调用点 | 领域单测、真实 PG 生命周期/归属集成、HTTP 契约/严格输入/作用域转发 |

## 验证

### 领域单测（`internal/reporting`）

`dashboard_folder_test.go` 覆盖创建规范化（trim、根/子级）、五类畸形输入拒绝（坏环境、空/超长/NUL 标题、坏父 UUID）、更新改名、列表作用域闭集（`""`/`root`/UUID 放行，`bogus`/`ROOT`/坏 UUID 拒绝）。

### 真实 PostgreSQL 集成（隔离 schema，`-tags=integration`）

- `TestDashboardFolderLifecycle`：根→子→孙三级深度；第四级 `DASHBOARD_FOLDER_TOO_DEEP`；缺失/跨环境父级 `DASHBOARD_FOLDER_PARENT_NOT_FOUND`；幂等重放与同键异内容 `IDEMPOTENCY_CONFLICT`；环境隔离列表；改名修订与 `DASHBOARD_FOLDER_REVISION_CONFLICT`；直接 SQL 改父级被 23514 触发器拒绝；非空 409、空文件夹可删且删除后 404。
- `TestDashboardFolderDashboardAssignment`：带文件夹创建并回读、缺失文件夹 fail-closed；UUID/root/无过滤三种列表谓词；非法作用域拒绝；草稿 PATCH 移动与回归根级；非空删除 409 且裸 SQL 删除被 23503 RESTRICT 阻断。

### HTTP 边界（`internal/transport/control`）

新增假服务测试：五路由状态码/写权限/ETag/204 空体；缺环境、未知参数、坏 UUID、未知 body 字段、缺 If-Match、DELETE 带查询全部在触达服务前 400；看板列表 `folder_id`（UUID/root）正确透传，非法值 400。

### 变异红绿（三轮，sha256 逐字节还原）

基线哈希记录于 `/tmp/evia-check/dashboard-folders-baseline.sha256`：

1. 短路深度守卫 → 第四级插入退化为由 DB CHECK 报原始 23514，集成测试如期变红（证明 Go 层负责稳定错误码）；
2. 短路空删的子文件夹计数 → 非空删除退化为由自引用外键报原始 23503，集成测试变红；
3. 短路看板创建的文件夹归属校验 → 幽灵 folder_id 退化为由 `dashboards_folder_fkey` 报原始 23503，集成测试变红。

三轮均用 Edit 精确还原，`sha256sum -c` 三个文件全部 OK，复跑转绿。

### 门禁

- 完整 `make check` 退出码 0：32 个 Go 包 ok、0 FAIL；Node 测试 18/18、39/39、57/57；gofmt、prettier、命名检查、OpenAPI 契约校验、codegen 幂等全绿；
- 全量 PG 集成包 `-count=1 -skip 'TestFailoverBaselineWrite'` 新鲜通过（85.99s，0 FAIL）。`TestFailoverBaselineWrite` 属既有多主机环境门控，与本切片无关。

## 明确未做（边界）

- 控制台文件夹树/移动 UI 未实现，本切片为后端/契约/客户端能力；
- 不支持文件夹重挂父级（reparent）与子树移动，需要时以新建+移动看板+空删组合完成；
- 无同级手工排序字段，列表按 UUID 确定性排序，控制台可按标题客户端排序；
- 通用参数绑定、比较期、跨请求结果缓存仍为独立缺口。
