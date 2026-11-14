# 设计：sys.* 系统事件目录运行时注册

## 背景与现状

浏览器 SDK 已能产生五个保留系统事件（见 [browser-sdk.md](browser-sdk.md) 与
[system-events.json](../contracts/fixtures/system-events.json)）：
`sys.page_view`、`sys.page_scroll`、`sys.identity_link`、`sys.session_end`、
`sys.operation_failed`。它们全部 opt-in/显式、受发布门控、公开 `emit()` 禁伪造。

但服务端目前**没有任何运行时目录条目**承载这些事件，因此即使 SDK 上报，采集器也会因
来源目录中不存在对应 `event_key` 而拒绝入库。这是 SDK 能力到可查询事实之间的真实缺口，
属于后端/目录切片，不应在未评审前直接改核心链路。

## 硬阻塞（已核对代码）

1. **事件键约束拒绝点号**。迁移 `002_catalog.sql`：
   `catalog_events.event_key text CHECK(event_key ~ '^[a-z][a-z0-9_]{0,63}$')`，
   该正则不接受 `sys.page_view` 这类带点键，系统事件当前无法写入目录表。
2. **目录是项目级**。`catalog_events` 主键为 `(project_id, event_key)`，事件定义按项目
   发布；而系统事件语义上是跨项目的平台保留能力，两者模型不一致。
3. **采集链路按来源解析用户目录**。`ingestion/collector.go` 经
   `SourceResolver.ResolveSource(write_key)` 取来源，再用来源编译出的目录计划做 JSON Schema
   校验；没有为保留 `sys.*` 键预留旁路。

## 必须保持的不变量

- 保留键只能由 SDK 内部对应能力产生，公开自定义事件 API 继续拒绝 `sys.*`。
- 系统事件仍受与自定义事件一致的有界 Schema、来源授权与发布资格约束，不能绕过校验。
- 不破坏既有自定义事件的存储与校验路径（默认行为逐字节稳定）。
- 系统事件不采集凭据、原始异常或请求正文（契约已固化，且有 Go 不变量测试守护）。

## 候选方案

### 方案 A：独立系统目录表 + 保留系统来源（推荐）

新增 `system_catalog_events`（全局、无 project_id）存放五个 `sys.*` 定义，并引入一个内部
“系统来源”解析概念；采集器对 `sys.` 前缀键改走系统目录校验，用户目录路径与
`catalog_events` 约束完全不动。

- 优点：用户目录约束与权限模型零变更；系统事件全局一致、可独立版本化；职责清晰。
- 代价：新迁移与新的采集分支；需要定义系统来源如何与现有 write_key/来源授权衔接。

### 方案 B：放宽 event_key 约束并按项目播种

修改 `catalog_events` 检查以允许 `sys.` 前缀，再由运维向每个项目播种系统事件。

- 优点：复用现有目录/发布/采集代码，改动集中。
- 代价：放宽共享表约束的回归面大；每项目播种与升级是运维负担；系统能力被错误地项目化。

### 方案 C：无点别名映射

目录内用无点别名（如 `sys_page_view`）存储，采集器在边界做 `sys.` 键与别名的双向映射。

- 优点：无需改约束。
- 代价：引入命名映射层，事件键在 SDK/存储/查询间不一致，长期可读性与审计成本高，不推荐。

## 推荐与待拍板决策

倾向**方案 A**。落地前需要业务/架构侧确认：

1. 系统来源与现有 `ingestion_sources`/write_key 的关系：复用浏览器来源的 origin 授权，还是
   独立的保留来源标识？
2. 系统事件是全局单版本，还是仍需按环境（development/staging/production）区分发布？
3. 系统事件在 ClickHouse 的落库是否与普通事实同表同列（现有 event_facts 已含页面/导航上下文字段）。
4. 迁移在本沙箱受新文件读取稳定性限制，需在能可靠应用迁移的环境执行并补真实 PG/采集集成验证。

## 测试与验收

- 采集器对 `sys.` 前缀走系统目录、对自定义键保持原路径的单元与真实 PG/HTTP 集成测试。
- 未注册/未发布系统事件 fail-closed；系统来源禁用时拒绝。
- 五个事件端到端：SDK 上报 → 采集校验 → 落库 → 按原始身份/页面上下文可查询。
- 既有 `TestGoldenContracts` 与本次新增 `TestSystemEventFixtureInvariants` 保持绿色。
