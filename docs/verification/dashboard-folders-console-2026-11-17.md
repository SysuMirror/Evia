# 看板文件夹控制台接线验证记录（2026-11-17）

## 问题（确定性缺口）

迁移 035 与 `dashboard-folders` API 已交付后端能力，但控制台只有平铺看板列表：无法浏览文件夹树、新建/改名/删除文件夹，也无法在创建或草稿阶段指定看板归属、或把已有看板移动到其他文件夹。本切片为纯前端接线，复用既有 API 与手写客户端方法，不引入契约变更。

## 范围与语义

- **树浏览**：列表页左侧文件夹栏提供「全部看板」（无过滤）、「未归档（根级）」（`folder_id=root`）与按标题排序、按 depth 缩进的文件夹节点；选中节点驱动看板列表的 `folder_id` 查询参数，游标与该范围绑定（切换范围重置分页）。
- **文件夹管理**：新建根/子文件夹（父级下拉仅列 depth<3 的节点）、改名（If-Match 修订）、空删确认 Modal；服务端 409 `DASHBOARD_FOLDER_NOT_EMPTY` 与深度错误通过页面级 `CatalogError` 呈现。
- **看板归属**：草稿编辑器新增「所属文件夹」选择器，工作区状态持有 `folderID`，脏检查包含归属变化；创建体与草稿 PATCH 全量替换 `folder_id`，空字符串回归根级（线上 JSON 省略字段而非发 null）。
- **移动看板**：列表行「移动」Modal，先读当前草稿取得修订与定义，再以同一 PATCH 提交新归属，避免覆盖定义。
- **权限与冻结**：无 `dashboards.write` 时所有写控件禁用；请求进行中 Modal 不可关闭、按钮冻结；401/403 走共享 `perform` 的 deny 路径。

## 改动清单

| 文件 | 说明 |
| --- | --- |
| `dashboards/use-dashboard-folders.ts`（新） | 文件夹 react-query 读取与创建/改名/删除变更，统一失效前缀 |
| `dashboards/folder-tree.tsx`（新） | 树节点、范围切换、三个管理 Modal、深度提示 |
| `dashboards/move-dashboard.tsx`（新） | 看板移动目标选择 Modal（根级 + 全部文件夹） |
| `dashboards/types.ts` | `DashboardWorkspace.folderID`、`DashboardFolderScope` |
| `dashboards/use-history.ts` | `folderScope` 状态并入列表 query key 与 `folderId` 参数 |
| `dashboards/use-draft.ts` | 创建/保存体携带 `folder_id`、脏检查、`changeFolder` |
| `dashboards/use-controller.tsx` | 文件夹 hook 装配、带作用域的新建、`moveDashboard`（读草稿后 PATCH） |
| `dashboards/draft.tsx`、`list.tsx`、`dashboards.tsx`、`styles.css` | 编辑器文件夹选择器、行移动按钮、双栏布局与响应式 |
| `packages/api-client` | `DashboardPage.folderId` 类型与两个新客户端测试（CRUD 编码、归属体） |

## 验证

### 静态与构建

- `pnpm typecheck`（全工作区 tsc）退出码 0；`pnpm build` 生产构建成功；prettier 全量通过。
- 客户端测试 41/41 通过（本轮新增 2 个：文件夹 CRUD URL/头部/204 空体断言；创建与草稿更新的 `folder_id` 线格式，含根级省略字段而非 null）。

### 变异红绿（两轮，sha256 逐字节还原）

基线哈希记录于 `/tmp/evia-check/dashboard-folder-console-baseline.sha256`：

1. 创建体 `folder_id` 改为数值 → tsc TS2345（载荷不再满足 `DashboardCreate`），证明契约类型约束了归属字段；
2. 文件夹创建回调丢掉 `parentID` → tsc TS2345（不满足 `{title,parentID}` 载荷），证明树组件与数据 hook 的父子级传递不可静默丢失。

两轮均用 Edit 精确还原，`sha256sum -c` 四个文件全部 OK，复跑 tsc 与客户端测试转绿。

### 门禁

完整 `make check` 退出码 0：32 个 Go 包 ok、0 FAIL；Node 测试 18/18、41/41、57/57；gofmt、prettier、命名、契约校验、codegen 幂等全绿。后端行为由上一切片的真实 PG 集成测试（`TestDashboardFolder*`）持续覆盖，本切片无后端改动。

## 明确未做（边界）

- 控制台无组件级单测设施，文件夹交互以 tsc 类型守卫、客户端请求测试与既有手动浏览器链路为证据，全栈 e2e 属凭证门控不在默认门禁；
- 不支持拖拽排序/移动，重组通过 Modal 显式选择目标文件夹；
- 通用参数绑定、比较期、跨请求结果缓存仍为独立缺口。
