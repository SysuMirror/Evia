# Evia 开发交接文档

交接日期：2026-11-27（Asia/Shanghai）。面向继续开发本仓库的工程师与 coding agent。

这是一份持续演进项目的开发交接基线，不是重新立项方案。请沿用现有架构、契约、组件体系与验证门禁按模块推进。文中“已实现”仅指明确列出的行为与已落仓证据，不代表已完成跨机房生产容量或千万用户规模验收。

## 1. 接手速览

| 项目 | 当前状态 |
| --- | --- |
| 仓库 | <https://github.com/SysuMirror/Evia>（原 `howtomakeaname/Evia` 已重定向） |
| 主分支 | `master`，小步 Conventional Commits 提交并推送 |
| 近期关键功能提交 | `2b5b4bc`–`cab772d`（端到端 A/B 实验）、`22d0503`（弱 ETag `304` + 配置缓存 + 并发边界调优）、`3a2588a`（README 全面更新） |
| PostgreSQL 当前迁移 | `001` 至 `054_experiments.sql`（以后续 `backend/internal/storage/postgres/migrations/` 最新编号为准） |
| ClickHouse 当前迁移 | `001` 至 `009_dedicated_layout_view.sql`（独立迁移链；`evia property-slots` 已生产化并在百万查询矩阵实现 `728/728`） |
| 最近交付主线 | 账号自助与邀请（`052`）、OAuth2 PKCE 登录（`053`）、端到端 A/B 实验（`054`）、采集/配置吞吐优化（`22d0503`）、控制台视觉与导航升级 |
| 当前核心优化与缺口 | 持续高压写入吞吐优化（`ingest-worker` 攒批与 `CachedSources` `singleflight`）、分析下推 ClickHouse 原生 SQL（突破 10 万行 Go 评估器上限）、弱网会话回放性能、`@evia/browser-sdk` npm 公开发行、跨故障域生产 HA 与 Flink 流计算 |

接手前先执行 `git status`、`git log -n 10 --oneline` 与 `git fetch` 确认最新远端状态，避免覆盖已有工作。

优先阅读顺序：

1. [AGENTS.md](AGENTS.md)、[README.md](README.md)、[PROGRESS.md](PROGRESS.md)。
2. [功能覆盖清单](docs/feature-coverage.md) 与 [生产就绪证据索引](docs/production-readiness.md)。
3. [API 规范](docs/standards/api.md)、[开发验证规范](docs/standards/development.md)、[UI 规范](docs/standards/ui.md)。
4. 对应任务领域文档、目录级 `AGENTS.md` 与 `.agents/skills/platform-*`。

## 2. 产品目标与持续有效的开发约束

### 产品目标

Evia 映途是自托管事件采集、用户行为分析与在线实验平台，基于开源组件构建。业务方在平台注册事件与属性 Schema、校验并发布激活后，通过浏览器 SDK 或服务端接口上报；平台提供事件治理、多维行为分析、版本化指标、保存分析、看板、定时报告、身份解析、加密会话回放与 A/B 实验闭环。UI 与外部接入 Agent 共享同一套有版本、可授权、可验证的 API。

容量讨论必须把“总用户规模”“同时在线”“瞬时/持续事件吞吐（EPS）”与“分析查询并发”分开建模与实测，不混用口径。

### 工程协作方式

- **契约与服务端优先**：先改 `contracts/` 并重新生成 Go/TypeScript 类型，再完成领域策略、持久化、HTTP 边界与真实依赖测试，最后联调前端。不用空壳页面或假数据代替能力交付。
- **小步聚合提交**：遵循 Conventional Commits（`type(scope): imperative description`），实现与对应测试同提交，不夹带无关改动。
- **文档与证据同步**：保持 `README.md`、`HANDOFF.md`、`PROGRESS.md` 与验证记录同步；历史验证报告保留原始测试条件，不改写为后续版本结论。
- **命名与品牌约束**：第一方命名统一使用 `event`、`activity`、`journey`、`emit` 等词汇，必须通过 `tooling/check_naming.py`；不在源码、文档、测试、资产或提交信息中写入外部界面参考产品名称、业务私有域名或生产凭据。
- **内外资产分离**：内部维护 skill 在 `.agents/skills/platform-*`，对外接入 skill 在 `integrations/skills/activity-*` 并通过白名单打包。

### 前端要求

- 采用 React 与 Evia 自建设计系统（`packages/design-system/`），普通表单与交互控件不引入通用第三方 UI 库；专业图表（ECharts）按需加载。
- 交互组件按行为契约验证：hover/focus/active/disabled/busy/invalid/read-only、键盘导航、异步焦点恢复、移动端布局与未保存保护。
- 文案避免装饰性冗余与中间点分隔符，并列项使用逗号、竖线或斜杠。

## 3. 技术栈与仓库地图

### 核心组件职责

| 组件 | 职责 | 当前边界 |
| --- | --- | --- |
| Go (`backend/`) | 管理/采集 API、发布交付探针、9 类后台 Worker 与运维 CLI | 单一 Go module，通过 `bin/evia <subcommand>` 分别运行 |
| PostgreSQL | 账号/项目/权限、事件目录、发布、实验配置、指标/分析/看板/报告、身份图快照、任务队列与审计 | 控制面与元数据真源 |
| Kafka | 采集确认后的事件缓冲、就绪探针与死信隔离队列 | 生产多副本容灾需在多故障域环境独立验收 |
| ClickHouse | 原始事件事实、去重读视图、热点属性专用列与分析执行 | 已通过百万行 `728/728` 矩阵，部分复杂分析仍由 Go 拉取 10 万行评估 |
| Redis | 跨实例分布式接入配额令牌桶 | 生产 HA 演练已完成单宿主 Sentinel/Cluster 验证 |
| React / TypeScript | 控制台、设计系统、生成式 API Client | 图表按需加载 ECharts |
| 浏览器 SDK | 零运行时依赖 ESM 包（`@evia/browser-sdk`） | 当前提供工作区源码与本地构建，尚未发布 npm |
| Flink | 后续有状态流计算方向 | 独立规划项目，当前非运行时依赖 |

事件主链：`SDK / 服务端` -> `POST /v1/collect`（`evia serve` + Redis 配额） -> `Kafka` -> `ingest-worker`（擦除屏障 + 落盘） -> `ClickHouse`。查询与实验统计通过 PostgreSQL 任务队列由 `query-worker` 异步执行。

```text
backend/
  cmd/evia/                       CLI、配置与全部子命令运行时装配
  cmd/evia-bench/                 事件上报压测器
  internal/access/                会话、OAuth2 PKCE、邀请、成员/设置/审计、机器身份、账号运维
  internal/catalog/               事件与字段定义、草稿、校验、不可变发布
  internal/delivery/              发布交付、探针租约、激活、签名公开配置与缓存
  internal/ingestion/             来源鉴权缓存、接入预算、系统事件目录、批量接收
  internal/pipeline/              Kafka 读写、ClickHouse 落盘、擦除屏障、热点列回填、滞后监控
  internal/analysis/              查询校验/计划/执行（计数/UV/趋势/行为链/漏斗/留存/分群/路径/归因/热力图/实验）
  internal/experiments/           A/B 实验配置、状态机、分流规则与校验
  internal/reporting/             能力发现、保存分析、版本化指标、公式 DAG、看板
  internal/reports/               定时报告计划与执行
  internal/identity/              版本化身份图折叠、快照与清理
  internal/content/               加密内容分段组装与受控读取（支撑会话回放）
  internal/erasure/               合规主体擦除与防复活墓碑
  internal/retention/             数据保留窗口清理策略
  internal/archiving/             冷数据归档清单元数据
  internal/exporting/             CSV / NDJSON / Parquet 异步导出
  internal/backfilling/           历史数据受控回填
  internal/overview/              项目概览有界实时汇总
  internal/quotas/                项目/环境配额覆盖管理
  internal/diagnostics/           管道与采集健康诊断
  internal/propertycatalog/       热点属性专用列目录规划
  internal/propertylayout/        属性词法 token 映射与布局编译
  internal/artifacts/             导出与任务产物安全访问
  internal/storage/postgres/      PostgreSQL 持久化适配、迁移（001–054）与真实库集成测试
  internal/transport/control/     管理 API（/api/v1/*）与公开 API（/v1/*）HTTP 适配
  internal/platform/              配置、错误、游标、契约校验、预算、观测指标与优雅排空
  internal/gen/wire/              OpenAPI/JSON Schema 生成的 Go 线缆类型
apps/console/                     React 控制台业务工作区与公开落地页
packages/design-system/           自有设计令牌、原生控件、日期/下拉弹层与 ECharts 封装
packages/api-client/              生成的 OpenAPI 类型与统一请求客户端
packages/browser-sdk/             零运行时依赖的浏览器采集与 A/B 实验 SDK
contracts/                        OpenAPI、事件信封、查询与报表 JSON Schema 及系统事件样例
deploy/                           Docker Compose 依赖、ClickHouse DDL（001–009）与 HA 配置
tooling/                          代码生成、契约/命名/边界/监控检查、压测、备份与验证脚本
tests/browser/                    Playwright 组件、页面与全栈浏览器测试
.agents/skills/platform-*/        平台内部开发维护 skills
integrations/                     公开业务接入 skills、CLI 工具与白名单打包
docs/                             架构、领域设计、工程规范、运维 Runbook 与验证记录
```

锁定工具版本：Go `1.27.3`、Node `24.22.1`、pnpm `11.25.0`；真实依赖基线为 PostgreSQL `17.9`、Kafka `4.4.0`、ClickHouse `26.x`、Redis `8.10.x`。

## 4. 已实现能力与剩余缺口

| 模块 | 当前已实现能力 | 重要限制或待完善项 |
| --- | --- | --- |
| 账号、会话与治理 | 密码登录、OAuth2 授权码 + PKCE S256（`053`）、账号自助与邀请链接（`052`）、成员角色、项目设置、操作审计、运维 `account-*` 命令与治理控制台 | 完整企业多租户组织层级、SSO 多提供商 UI 管理 |
| 事件目录与发布 | 事件/字段 Schema、样例校验、草稿修订、不可变发布、Kafka->CH 就绪探针与租约激活、7 个内嵌 `sys.*` 系统事件端到端贯通 | 跨版本字段重命名映射、细粒度敏感字段分级策略 |
| 采集与公开配置 | 浏览器 Origin 与服务端 Bearer 隔离、凭据轮换/撤销、Redis 分布式令牌桶、`/v1/config` Ed25519 签名 + 强/弱 ETag `304` + 渲染缓存、Kafka 256 并发槽 | `CachedSources` 2s 过期需补 `singleflight`；`ingest-worker` 128 条同步小批写 CH 在持续高压下需攒批优化 |
| 多维行为分析 | 计数、精确/近似 UV、趋势（分钟/小时/天/周）、行为链（快照游标 + 引擎级时间线续拉）、有序漏斗、日/周/月留存、分群、聚合路径、5 模型归因、点击热力图（含元素排名与多视口） | 漏斗/留存/分群/路径/归因/实验仍走 10 万行 Go 拉取评估，中高 DAU 场景需下推 ClickHouse 原生 SQL |
| 端到端 A/B 实验 | 实验 CRUD 与状态流转（`054`）、运行态实验写入签名 `/v1/config`、SDK 确定性 FNV-1a 分流与自动曝光、内置统计分析（SRM 卡方、转化率 Z 检验、数值均值 Welch t 检验与 95% CI）、控制台「A/B 实验」工作区 | 实验定时自动停启、多指标护栏联动、互斥层/正交分流域 |
| 身份图 | `identity-worker` 周期折叠（`043`）、历史修订快照与清理（`051`）、`resolved_person` 查询物化展开、与擦除屏障联动防复活、控制台「身份修订」工作区 | 大规模图展开预算扩展、实时流式身份归并（规划归入 Flink） |
| 加密会话回放 | `sys.content_chunk` 混合加密（`044`–`050`）、4 KiB 固定填充、v2 精确 light-DOM Mutation 播放与多章节 fail-closed resync、有界嵌入表面（Shadow DOM/iframe/canvas/media）、敏感内容访问审计（`050`）与控制台播放器 | 移动弱网/长会话吞吐优化（见 [回放容量验证](docs/verification/session-replay-capacity-2026-11-22.md)） |
| 指标、保存分析、看板与报告 | 不可变原子指标、复合公式指标 DAG（含封闭时间单位换算）、保存分析（归档/搜索/导出）、版本化看板（1–12 组件、文件夹、比较期、参数、文本卡、公式指标引用）、定时报告 | 广义货币/自定义量纲换算、看板级查询结果共享缓存 |
| 数据运营与合规 | CSV/NDJSON/Parquet 异步导出（`039`/`041`/`042`）、数据保留与归档、合规主体擦除与墓碑屏障、历史回填、配额覆盖、诊断面板、热点属性专用列 `property-slots` | 真实对象存储厂商多段上传与 SSE-KMS 联调 |
| 浏览器 SDK 与对外接入 | `@evia/browser-sdk`（自动页面/身份关联/会话结束/滚动/热力图点击、持久队列、跨文档会话连续性、有界批量排空、A/B 本地分流、加密分段上报）与公开接入 Skills（`integrations/`） | npm 正式公开发布、SDK 重试退避补随机 Jitter |
| 生产工程与可观测性 | 进程优雅排空、单宿主 PG/Redis/CH HA 演练、备份恢复演练、Prometheus/Alertmanager/Grafana 防漂移门禁 | 多机房/跨故障域真实拓扑、TB 级多 Part headroom 压测 |

完整逐项状态见 [功能覆盖清单](docs/feature-coverage.md)。

## 5. 近期关键模块续开发入口

### 5.1 端到端 A/B 实验（迁移 `054_experiments.sql`）
- **核心代码**：
  - 契约与生成：`contracts/http/openapi.json`、`contracts/query/query-request.schema.json`、`contracts/reporting/saved-analysis-template.schema.json`
  - 领域与存储：`backend/internal/experiments/experiments.go`、`backend/internal/storage/postgres/experiments.go`、`backend/internal/transport/control/experiments.go`
  - 配置下发：`backend/internal/delivery/{types,service}.go`、`backend/internal/transport/control/delivery.go`（运行态实验序列化进 `PublicConfig.experiments`，纳入 ETag 与 Ed25519 签名覆盖）
  - 统计分析：`backend/internal/analysis/experiment.go`（`QueryKindExperiment = "experiment"`，支持意图治疗首次曝光锁定、跨组污染检测 `contaminated_subjects`、SRM `p < 0.001` 告警、转化率双比例 Z 检验与数值均值 Welch t 检验）
  - SDK 与控制台：`packages/browser-sdk/src/{experiments,client}.ts`、`apps/console/src/experiments/experiments.tsx`

### 5.2 采集与配置高并发调优（提交 `22d0503`）
- `backend/internal/transport/control/delivery.go`：兼容 RFC 7232 弱 ETag（`W/"<64-hex>"`），修复反向代理开启 gzip 后 `If-None-Match` 被拒为 `400 INVALID_ETAG` 的问题。
- `backend/internal/delivery/service.go`：增加有界内存 `renderedCache`（256 项），缓存规范哈希、Ed25519 签名与配置事件列表，同时保留实时来源撤销与实验读取。
- `backend/internal/pipeline/kafka.go`：`KafkaProducer` 并发槽由 `16 -> 256`，`MaxBufferedRecords` 由 `5000 -> 20000`，`MaxBufferedBytes` 由 `32MB -> 64MB`。
- `backend/internal/storage/postgres/database.go`：`pgxpool` 默认 `MaxConns 20 -> 64`、`MinConns 2 -> 8`。

### 5.3 账号邀请与 OAuth2 登录（迁移 `052`、`053`）
- 入口：`backend/internal/access/` 与 `apps/console/src/{auth,invite,account}/`。支持项目邀请链接与标准 OAuth2 Authorization Code + PKCE S256 登录流程。

### 5.4 热点属性专用列（`evia property-slots`）
- 入口：`backend/internal/propertycatalog/`、`backend/internal/propertylayout/`、`backend/internal/pipeline/property_slots.go`、`backend/internal/analysis/`。
- 验证证据见 [百万查询容量修复验证](docs/verification/query-capacity-fix-2026-11-20.md)：在同数据集百万行矩阵下将 `526/728` 修复至 `728/728` 零失败。

## 6. SDK 与业务接入边界

入口为 `packages/browser-sdk`（导出 `createEviaClient`、`assignExperimentVariant`、`hashExperimentBucketBps`、`EviaSDKError`），详细语义见 [浏览器 SDK 文档](docs/browser-sdk.md)。必须维持以下契约边界：

- `emit()` 同步捕获事件、身份、页面与属性快照后异步发送；重试保持原始 `event_id` 与字节语义，不静默升级发布版本。
- `identify(uid)` 仅作用于后续事件（跨设备/历史归并由服务端 `identity-worker` 与 `resolved_person` 查询处理）；`reset()` 轮换匿名/会话/页面范围，不影响已入队事件。
- 所有自动采集（`autoPageViews`、`autoIdentityLink`、`autoSessionEnd`、`autoPageScrolls`、`autoHeatmapClicks`）以及 `persistQueue` / `persistSession` 均默认为 `false`（opt-in），公开 `emit()` 禁止伪造 `sys.*` 保留事件键。
- `evaluateExperiment()` 仅依赖本地已拉取的公开配置进行确定性分流（零额外网络 RTT），默认按 `(experiment_key, version, session, subject)` 去重自动上报曝光事件，并可传入 `exposureProperties` 补齐严格 Schema 字段。
- `flush()` 仅代表有界本轮发送；端到端送达应以单条 `Emission` 的结果为准，且 `202 accepted` 仅代表 Kafka 持久确认，不等价于 ClickHouse 立即可查。

## 7. 必须保持的核心工程约束

### 契约与服务端
- 权威定义在 `contracts/`，修改后必须执行 `make generate` 并通过 `make generated-check`，不手改生成文件。
- 领域层不直接依赖 HTTP 或 SQL；持久化层不直接复用生成 wire 结构体。
- 所有写入与查询在事务边界内先复查认证与项目/环境权限，再处理幂等键重放；不得信任客户端透传的 scope 或陈旧权限缓存。
- 人类会话、机器 Bearer、浏览器来源写 key、服务端来源写密钥四者严格隔离，混合凭据不回退。
- 数据库迁移严格向前追加（下一个编号 `055_*.sql`），绝不修改已发布迁移；`evia serve` 不自动执行迁移。

### 数据与查询语义
- 明确区分五个阶段：采集回执（`202`）、Kafka 落盘、ClickHouse 可见、去重读视图一致、查询任务完成。
- 去重主键受 `(project_id, environment, source_id, event_id)` 约束；优化物理读不得破坏缺失、`null`、`false`、`0`、空字符串与数组的类型语义区别。
- 所有新增分析或报表能力复用统一项目查询预算（默认并发上限与有界结果），不得绕过保护护栏。

### 前端与安全
- 保持包依赖单向：`design-system` 不依赖业务 API 类型，`api-client` 不依赖 React，`browser-sdk` 不依赖控制台。
- 涉及密钥、Token 或敏感内容回放的自动化测试关闭未审查 trace/video，不把凭据、`.env` 或真实业务数据提交进 Git。

## 8. 真实验证基线与已知瓶颈

- **百万查询容量矩阵**：通过 `evia property-slots` 专用列 + 全命中剪 Map + 严格模式，`728/728` 零失败（见 [query-capacity-fix-2026-11-20.md](docs/verification/query-capacity-fix-2026-11-20.md)）。
- **单机生产韧性门禁**：优雅排空、PG/Redis/CH 单宿主 HA、备份恢复、保留 headroom 模型、多租户单查询 512 MiB 封顶、Prometheus/SLO 门禁均已闭环（见 [production-readiness.md](docs/production-readiness.md)）。
- **已识别的高并发吞吐与扩展瓶颈（下一阶段优先关注）**：
  1. **持续高压写入链路**：单 `ingest-worker` 当前以 `BatchSize = 128` 串行执行 `Kafka Poll -> Postgres BarrierSink 查询 -> ClickHouse async_insert=0 同步 INSERT -> Kafka Commit`，持续 >3k–5k EPS 时会高频制造小 MergeTree Part 并拉大 Kafka Lag。
  2. **2 秒来源缓存过期抖动**：`backend/internal/ingestion/cache.go` 中 `CachedSources.ResolveSource` 缺少 `singleflight` 合并回源，仅靠 `misses`（容量 8）限流，高并发下每 2 秒过期窗口会触发少量 `503 SOURCE_CONFIGURATION_BUSY`。
  3. **SDK 限流重试波峰**：`packages/browser-sdk/src/client.ts` 退避计算尚未加入随机 Jitter，极端同步脉冲被 `429` 拦截时容易引发波浪式同步重试。
  4. **中高规模复杂分析**：`funnel`、`retention`、`segment`、`path`、`attribution`、`experiment` 仍由 Go 侧拉取最多 10 万行原始事件做内存聚合，数据规模跨过万级 DAU 后需逐步下推 ClickHouse 原生 SQL。

## 9. 建议下一步工作包

| 优先级 | 工作包 | 关键入口与验收目标 |
| --- | --- | --- |
| P0 | **极端持续吞吐优化** | `backend/internal/pipeline/{worker,clickhouse,erasure_barrier_sink}.go`、`backend/internal/ingestion/cache.go`、`packages/browser-sdk/src/client.ts`：`ingest-worker` 按批大小+时间窗攒大批次（2000–5000 行）并优化墓碑查询缓存；`CachedSources` 补 `singleflight`；SDK 指数退避补随机 Jitter；用真实 Kafka+ClickHouse 压测验证持续万级 EPS 零 503 与低 Part 速率 |
| P1 | **复杂分析下推 ClickHouse 原生 SQL** | `backend/internal/analysis/{experiment,funnel,retention,segment,path,attribution}.go`：优先将实验统计与漏斗/留存核心聚合下推 ClickHouse，解除 10 万行拉取截断并保持与现有一致性单测字节等价 |
| P1 | **会话回放弱网与容量收敛** | `docs/verification/session-replay-capacity-2026-11-22.md`：解决弱网超时重试与预算计费可观测性，补齐 4 格矩阵门禁 |
| P2 | **SDK 发布与外部生产化** | `@evia/browser-sdk` 正式打包发布流程、真实对象存储多段上传/KMS 联调、跨故障域多机房 HA 验收与 Flink 流计算独立立项 |

## 10. 本地复现与质量门禁

按 [本地开发说明](docs/runbooks/development.md) 与 [依赖说明](docs/runbooks/dependencies.md) 准备本地 `.env`（`0600`）及依赖服务后：

```bash
# 编译二进制、显式迁移并初始化空库管理员
mkdir -p bin
go -C backend build -o "$PWD/bin/evia" ./cmd/evia
./bin/evia migrate
./bin/evia bootstrap

# 按需启动 API 与工作进程（完整链路至少需 serve + ingest-worker + query-worker）
./bin/evia serve
./bin/evia ingest-worker
./bin/evia query-worker

# 前端安装与控制台开发服务
pnpm install --frozen-lockfile
pnpm dev
```

常用质量门禁（按变更范围选择，CI 流程见 `.github/workflows/verify.yaml`）：

```bash
make generated-check     # OpenAPI / JSON Schema 生成代码一致性
make check               # 命名、契约、边界、监控、工具单测、Go vet/race、前端与 SDK 检查
make integration-test    # PostgreSQL 真实库竞态集成（需 EVIA_TEST_DATABASE_URL）
make pipeline-test       # Kafka -> ClickHouse 管道集成
make budget-test         # Redis 接入配额集成
make query-test          # ClickHouse 查询引擎集成
make browser-test        # Playwright 浏览器端到端（需 EVIA_E2E_*）
```

> 安全提醒：切勿在聊天、日志或提交中打印 `.env`、数据库密码、Cookie、Bearer Token 或一次性密钥；不要使用清空整个 Redis 实例或跳过权限校验的方式修复测试。

## 11. 接力启动提示模板

> 继续开发 Evia（`SysuMirror/Evia`）。先阅读 `HANDOFF.md`、`AGENTS.md`、`README.md` 与 `docs/feature-coverage.md`，核对 `master` 最新提交与迁移编号（当前已至 `054_experiments.sql`）。沿用 Go 单模块、PostgreSQL/Kafka/ClickHouse/Redis 与自建 React 设计系统方案，坚持契约先行、后端与真实依赖验证优先、前端联调跟进的顺序。保持不可变版本、权限与环境隔离、幂等键与 `tooling/check_naming.py` 命名规范，小步提交并同步更新进度与验证文档。

## 12. 文档索引

| 主题 | 入口 |
| --- | --- |
| 总览与记录 | [README](README.md)、[PROGRESS](PROGRESS.md)、[功能覆盖清单](docs/feature-coverage.md)、[生产就绪索引](docs/production-readiness.md) |
| 架构与规划 | [架构说明](docs/architecture.md)、[查询容量规划](docs/plans/query-capacity-plan.md)、[自定义看板方案](docs/plans/custom-dashboards.md) |
| 工程规范 | [API](docs/standards/api.md)、[开发与验证](docs/standards/development.md)、[UI](docs/standards/ui.md)、[前端模块](docs/standards/frontend-modules.md) |
| 内部维护 Skills | [API](.agents/skills/platform-api/SKILL.md)、[UI](.agents/skills/platform-ui/SKILL.md)、[验证](.agents/skills/platform-verification/SKILL.md) |
| 核心业务模块 | [发布](docs/releases.md)、[查询](docs/analysis.md)、[发现](docs/reporting-discovery.md)、[指标](docs/metrics.md)、[保存分析](docs/saved-analyses.md)、[看板](docs/dashboards.md)、[报告](docs/reports.md)、[身份图](docs/identity.md) |
| 权限与接入 | [机器访问](docs/machine-access.md)、[项目治理](docs/project-governance.md)、[浏览器 SDK](docs/browser-sdk.md)、[公开接入资产](integrations/README.md) |
| 运维与压测 | [开发](docs/runbooks/development.md)、[依赖](docs/runbooks/dependencies.md)、[部署](docs/runbooks/deployment.md)、[高可用](docs/runbooks/ha.md)、[备份恢复](docs/runbooks/backup-restore.md)、[压测](docs/runbooks/benchmark.md)、[SLO](docs/runbooks/slo.md) |
