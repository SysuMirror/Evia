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

## 决策收敛（2026-11-15，切片 1 落地口径）

复核代码后四个决策点均可由现有架构证据收敛，不再阻塞工程推进；方案 A 的存储形态进一步细化为
**A2：平台契约编译进二进制**，而非 A1 的独立 PG 表。

1. **来源关系**：不新建凭据主体。五个系统事件全部 browser-only，本就随浏览器来源的 write_key
   与 Origin 授权上报；采集器只在事件规则解析层对 `sys.` 前缀改走系统目录，`authenticate`、
   预算与拒绝统计路径零改动。
2. **版本模型**：全局单版本、无环境发布状态机。环境维度继续由来源记录带入落库 `environment`；
   平台契约升级与 SDK 协同、随二进制发版，不需要项目级 release/activation/lease 机制。
3. **ClickHouse**：同表同列零 DDL。`event_facts.event_key` 是无正则约束的
   `LowCardinality(String)`，信封契约 event_key pattern 已允许 `sys.` 前缀（maxLength 68），
   页面/导航上下文字段已齐备。
4. **存储形态（A2 取代 A1）**：系统事件是平台内置不可变契约，无 CRUD、无项目归属、无发布状态机，
   PG 表的事务与激活机制对其全部冗余，还会引入迁移播种与 fixture 双份真相。因此以
   `contracts/fixtures/system-events.json` 为单一真相，经 codegen 确定性生成到
   `backend/internal/gen/wire/system-events.gen.json` 并 `go:embed` 编译进二进制，漂移由
   generated-check 与 Go 语义对比测试双重守护。未来确需运行时可配置时，解析接口不变即可替换为
   PG 实现。原 A1（独立系统目录表）保留为备选，不再作为推荐。

## 落地切片拆分

- **切片 1（已交付，2026-11-15）**：系统目录持久化底座——codegen 生成 embed 固件、
  `internal/ingestion/systemcatalog.go` 加载器（`IsSystemKey`/`LoadSystemCatalog`/`Rule`，
  构造期 fail-closed）、闭集与 schema 编译测试。**不接线采集器**，默认行为逐字节不变，
  此阶段 SDK 上报 `sys.*` 仍按现状被项目目录拒绝。
- **切片 2（已交付，2026-11-15）**：采集器 `sys.` 前缀旁路——`Collector.WithSystemCatalog`
  可选装配（nil 时逐字节保持项目计划单路径行为）；系统键跳过 release plan 解析与租约准入，
  改查 embed 目录规则，仍依次过信封契约、来源 kind（五规则均 browser-only）、属性 JSON Schema、
  context 与批次预算，拒绝码与普通路径完全一致；`cmd/evia` 启动时加载系统目录，固件损坏即
  fail-closed 拒绝启动。
- **切片 3（待做）**：公开配置（`/v1/config`）纳入系统事件使 SDK 真正发送，再做端到端
  SDK 上报 → 采集 → Kafka → ClickHouse 落库 → 可查询的真实依赖验证与文档核销。

### `release_revision` 口径（切片 2 定稿）

系统事件不绑定任何项目发布，但信封契约要求 `release_revision >= 1`、pipeline 记录校验
（`record.go`）同样强制该下界、ClickHouse `release_revision UInt64` 非空。因此采集器对系统
事件**原样保留信封修订值仅作溯源**（SDK 发送时填来源当前项目发布修订），不据此查询项目计划、
不参与租约准入复核；系统记录经 `systemItems` 显式标记并在准入复核中旁路。该口径不引入新的
哨兵值，也不放宽 pipeline/CH 的既有不变量。

## 测试与验收

- 采集器对 `sys.` 前缀走系统目录、对自定义键保持原路径的单元与真实 PG/HTTP 集成测试。
- 未注册/未发布系统事件 fail-closed；系统来源禁用时拒绝。
- 五个事件端到端：SDK 上报 → 采集校验 → 落库 → 按原始身份/页面上下文可查询。
- 既有 `TestGoldenContracts` 与本次新增 `TestSystemEventFixtureInvariants` 保持绿色。
