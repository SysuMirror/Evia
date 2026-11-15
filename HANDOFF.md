# Evia 开发交接文档

交接日期：2026-11-09，Asia/Shanghai。面向继续开发本仓库的 coding agent。

这是一份已有项目的交接，不是重新立项方案。继续使用现有架构、契约、组件和验证工具，按模块补齐原始需求。文中“已实现”仅指明确列出的行为，不代表完整产品、生产就绪或千万用户容量已经达成。

## 1. 接手速览

| 项目 | 当前状态 |
| --- | --- |
| 仓库 | <https://github.com/howtomakeaname/Evia> |
| 主分支 | `master`，用户允许直接小步提交并推送，不必默认走 PR |
| 最近功能交付基线 | `a8c0f569ab8cb4de4bc10e4d24853e5247cab331`，简称 `a8c0f56` |
| 基线 CI | [42625615892](https://github.com/howtomakeaname/Evia/actions/runs/42625615892)，4 个任务全部成功 |
| 最近后端功能源码提交 | `ac81e3e`，之后主要为接入工具、验收、机器授权选项和文档 |
| PostgreSQL 当前迁移 | `001` 至 `012_metrics.sql` |
| ClickHouse 当前迁移 | `001` 至 `005_identity_projections.sql`，独立迁移链 |
| 最近新增能力 | 独立原子指标后端 10 个接口；上一批为项目治理后端 7 个接口及账号创建命令 |
| 当前重点缺口 | 指标公式、保存分析/看板引用指标版本、治理与指标管理界面、身份关联、生命周期、导出和大规模查询性能 |
| 当前工作树 | 写本文档前干净，功能基线已推送；交接文档提交仅修改文档 |

上表 SHA 是功能与验证快照；本文件自己的文档提交会让 `master` 再向前。接手时执行 `git status`、`git log`、`git fetch` 核对最新状态，不能仅凭聊天中的 SHA 覆盖别人后续工作。旧 subagent 已结束，没有需要继续等待的实现任务。

优先阅读：

1. [AGENTS.md](AGENTS.md)、[README.md](README.md)、[PROGRESS.md](PROGRESS.md)。
2. [功能覆盖清单](docs/feature-coverage.md)，核对已实现、部分实现和未实现。
3. [API 规范](docs/standards/api.md)、[开发验证规范](docs/standards/development.md)。
4. 当前任务对应的领域说明、scoped `AGENTS.md` 和内部维护 skill。
5. 长期设计可参考 [自定义看板方案](docs/plans/custom-dashboards.md)，其中明确标为规划的 AST、参数和接口不能当现成能力调用。

## 2. 用户目标与持续有效的要求

### 产品目标

Evia 映途是私有化部署的事件采集与行为分析平台，主要采用开源组件。目标面向约千万级用户的网站，但“用户量”“同时在线”“事件吞吐”和“分析并发”必须分别建模、测量，不能混为一个容量指标。

业务方在平台注册自定义事件与属性，校验、发布并激活后，通过前端 SDK 上报；平台提供事件筛选、PV/UV、趋势、漏斗、行为链和可自定义看板。UI 与业务方 Agent 应使用同一套有版本、可授权、可验证的 API。

首类业务是论坛：打开首页、滚动/曝光帖子、点击帖子、阅读停留、评论/回复、点赞评论、点赞/收藏帖子等。事件需携带明确注册的业务字段，并关联 UID/匿名标识、会话、页面、导航和顺序。页面文本、帖子标题、评论正文不是默认采集项，应由业务方明确设计字段与触发条件。前端观察到一次点击不等于服务端业务操作已成功。

### 开发方式

- **服务端优先**：契约、领域策略、持久化、HTTP、真实依赖验证先完成，再接前端。不要用空页面或假数据代替能力交付。
- 认真规划大仓和模块边界，不为追求一个最小闭环牺牲后续维护性；也不要一次重写全仓。
- 用户希望持续推进，常规可逆实现无需反复询问；真正影响业务语义、破坏性操作或缺少必要信息时再沟通。
- 小而完整的 Conventional Commits，格式 `type(scope): imperative description`；实现和必要测试可以同一提交，不要混入无关模块。
- 用户允许直接推送 `master`；如采用 PR，则完成检查后可按已有授权合并。不要在任务末尾只留本地源码。
- 保持 README、PROGRESS、能力清单与实际证据同步。历史验证记录保留原条件，不改写为后来的能力。
- 可并行使用 `gpt-6.1-sol high` 开发，浏览器验证可用 `gpt-6.1-sol low`。先分配不重叠的文件所有权，给够上下文，契约/生成与迁移各设单一负责人，Git 操作集中串行执行。
- 不在源码、文档、资产、测试或提交信息中写入用户提供的界面参考产品名称、截图或品牌。现有依赖的必要署名和许可证保持准确。
- 第一方名称使用 `event`、`activity`、`journey`、`emit` 等仓库词汇，通过 `tooling/check_naming.py`；不要自行引入另一套行为采集命名。
- 平台内部维护规则与对外接入 skill 必须分离，公开打包使用明确白名单。

### 前端要求

使用 React 和 Evia 自有原生 UI 组件。普通控件不要重新引入通用 UI 库；图表使用成熟专用库，通过独立入口按需加载。用户明确要求完整交互，不接受只换颜色和边框的重构。

页面保持清楚的层级、合适的信息密度、图表和真实状态。不要堆装饰性英文眉题、重复 subtitle、说明段落或空卡片，也不要为了“简洁”去掉必要功能。并列状态用逗号，不用中间点。按钮、focus、hover、按下、错误、禁用、只读、异步、键盘、焦点恢复和移动端都要验证。界面变更提供真实浏览器截图。

已有 logo 和 README 架构图，不必重新生成。图示维护见 [资产说明](docs/assets/README.md)。需要修改生成式品牌/架构图片时使用 ImageGen；实际性能图使用真实数据与绘图脚本，不能生成虚构测量图。

## 3. 技术栈与仓库地图

### 现有组件职责

| 组件 | 职责 | 当前边界 |
| --- | --- | --- |
| Go | 管理/接收 API、发布交付协调、写入及查询工作进程 | 单一 Go module，通过不同子命令运行 |
| PostgreSQL | 项目/权限、定义/版本、发布、审计、幂等、查询任务及有界结果 | 元数据与授权真源 |
| Kafka | 接收确认后的事件缓冲、交付探针、隔离消息 | 本地证据为单节点，未证明生产多副本耐久性 |
| ClickHouse | 事件事实、去重读路径、类型筛选与分析 | 本地单节点，性能仍有明确缺口 |
| Redis | 跨实例接收预算 | 用户已指定使用 Redis，不更换实现选型 |
| React / TypeScript | 控制台、API 客户端、原生设计系统 | 图表按需加载 ECharts |
| 浏览器 SDK | 框架无关、零运行时依赖的 ESM 包 | 当前为源码/本地打包，未发布 npm |
| Flink | 后续有状态处理方向 | 尚未交付，不是当前启动必需组件 |

事件主链为 SDK → 接收 API → Kafka → ingest worker → ClickHouse。管理配置和权限位于 PostgreSQL；查询 API 在 PostgreSQL 入队，query worker 读取 ClickHouse，再持久化有界结果。指标、保存分析和看板复用这条查询链，不各自实现 SQL 引擎或独立预算。

```text
backend/
  cmd/evia/                       CLI、配置、运行时装配
  cmd/evia-bench/                 实际上报压测器
  internal/access/               会话、成员/设置/审计、机器身份、账号准备
  internal/catalog/              事件定义、草稿和发布
  internal/delivery/             发布交付、探针、激活
  internal/ingestion/             接收策略
  internal/pipeline/              Kafka/ClickHouse 写入与恢复
  internal/analysis/              查询验证、计划、执行及语义
  internal/reporting/             发现、保存分析、看板、独立指标
  internal/storage/postgres/      持久化适配器、迁移、真实数据库测试
  internal/transport/control/     HTTP 适配器
  internal/platform/              HTTP/错误/配置/游标/契约/预算等基础能力
  internal/gen/wire/              生成类型及离线验证包
apps/console/                    业务页面与状态编排
packages/design-system/          自有控件、布局、交互状态和图表入口
packages/api-client/             生成契约与共用 HTTP 客户端
packages/browser-sdk/            对外浏览器 SDK
contracts/                       OpenAPI、事件/查询/报表 JSON Schema
deploy/                          依赖配置、ClickHouse DDL、开发运维工具
tooling/                         代码生成、验证、真实 HTTP 验收、压测
tests/browser/                   组件、页面与真实业务链浏览器测试
.agents/skills/platform-*/       平台内部维护 skills
integrations/skills/activity-*/   对外业务接入 skills
integrations/tooling/             清单、受限 API 请求和白名单打包工具
docs/                            架构、标准、运行手册、验证和规划
```

当前仓库锁定 Go `1.27.3`、Node `24.22.1`、pnpm `11.25.0`；真实依赖证据使用 PostgreSQL `17.9`、Kafka `4.4.0`、ClickHouse `26.8.36.3`、Redis `8.10.3`。以当前锁文件、Compose 镜像摘要及 CI 为准；不要为装环境顺便升级全套依赖。

## 4. 已实现能力与未完成部分

| 模块 | 已实现 | 尚未完成或重要限制 |
| --- | --- | --- |
| 会话与项目 | 登录、退出、项目创建/读取、成员范围校验、CSRF/Origin | 邀请/注册自助及完整组织治理（停用/恢复/密码重置已交付） |
| 事件与字段 | 自定义定义、Schema、样例校验、草稿修订、不可变版本 | 稳定字段身份、单位/敏感语义治理、跨版本兼容映射 |
| 发布交付 | 真实探针、就绪租约、激活比较、公开配置 | 发布本身不表示来源可发送，也不表示数据已可查询 |
| 来源 | 浏览器 Origin、服务端凭据、启停、轮换、未知结果恢复 | 更完整的接入运维与规模证据 |
| 上报与写入 | 有界批次、逐项回执、持久队列确认、重放去重、隔离与消费位置处理 | 生产多副本、长期运行和恢复验收 |
| 查询 | 类型筛选、计数、精确/显式近似 UV、趋势、原始身份行为链、有序漏斗 | 长历史/高并发、引擎级时间线续拉、最终统计与身份图（结果快照续页已交付） |
| 发现 | 已发布事件/字段目录、能力发现、无执行查询校验 | 成本模拟、预留执行预算、全字段语义治理 |
| 保存分析 | 草稿、校验/diff、不可变版本、事件依赖固定、幂等运行及控制台 | 当前直接固定事件版本，没有独立指标版本引用 |
| 自定义看板 | 1–12 个固定分析版本组件、12 列布局、统一时间、逐图过滤、原子入队、状态/取消、软归档生命周期（迁移 029，含 `view=active|archived` 列表视图）及控制台双视图/恢复 UI | 文件夹、通用参数、比较期、指标引用、查询复用和缓存 |
| 独立原子指标 | 10 个 API、单位兼容、事件依赖、版本、整段/趋势查询、机器授权 | 公式、单位换算、分析/看板引用、专用 UI、手写客户端方法 |
| 项目治理 | 7 个成员/设置/审计接口、运维 `account-create`/`account-disable`/`account-enable`/`account-reset-password`/只读 `account-audit` | 专用 UI、邀请/注册自助 |
| 机器访问 | 独立主体、单项目/环境、24 项明确权限、一次性凭据、轮换/撤销、审计与控制台 | 权限集合创建后不可变，扩大用途需替代主体 |
| 浏览器 SDK | 显式事件/导航、身份快照、有界队列、重试、逐项结果、真实跨域链 | npm 发行、跨文档连续会话、自动页面事件、离线持久队列 |
| 对外接入资产 | 3 份公开 skill、事件清单、API 工具、9 文件白名单包；工具已支持指标路径 | 完整自动发布 CLI、实际业务仓库验收、SDK 包发行 |
| 原生 UI | 现有项目、事件、发布、来源、分析、保存分析、看板、机器访问工作区 | 成员/设置/审计/指标等新模块页面尚缺 |
| 高级分析 | 当前只有上述已实现模型 | 留存、分群、聚合路径、归因、生命周期、热图、会话回放 |
| 数据运营 | 有审计事实及有界任务结果 | 导出/授权下载、保留、归档、回补、删除与恢复屏障 |

完整需求以 [功能覆盖清单](docs/feature-coverage.md) 为准。早期 30 个无运行时 API 占位已全部由正式接口交付（30/30 清零，最后交付的是[管理分析报告](docs/reports.md)五操作）；不应重新把未实现路径放回正式 OpenAPI。`sys.*` 五个保留事件已端到端打通：契约固件经 codegen embed 进二进制（`internal/ingestion/systemcatalog.go`，构造期 fail-closed），采集器 `Collector.WithSystemCatalog` 让系统键跳过项目发布计划、按 embed 规则过全部既有门控；`/v1/config` 经 delivery `ReservedEventProvider` 向浏览器来源下发五个保留事件（公开配置上限 205=200 项目+5 平台），SDK 真正发送，真实 Kafka→ClickHouse 集成测试验证可查询。决策与切片记录见 docs/design-sys-event-catalog.md。

## 5. 最近交付模块的续开发入口

### 5.1 独立指标，迁移 012

阅读 [指标说明](docs/metrics.md) 和 [指标验证](docs/verification/metrics-2026-11-09.md)。

主要文件：`backend/internal/reporting/metric*.go`、`backend/internal/storage/postgres/metrics*.go`、`backend/internal/transport/control/metrics*.go`、`backend/cmd/evia/runtime_metrics_test.go`、`tooling/verification/metrics_smoke.py`。

`MetricDefinition` 包含 `title`、`description`、`event_versions`、可选 `filter`、`subject`、`aggregation` 和 `unit`。聚合支持 `count/unique/sum/avg/min/max/p95`；默认精确，`unique/p95` 可显式近似。次数、原始 UID、匿名标识、不同字段值与数值单位有兼容约束。数值单位只是声明，不自动缩放数据。

令 `P = /api/v1/projects/{project_id}`，`M = P/metrics/{metric_id}`：

| 操作 | 接口 |
| --- | --- |
| 列表/创建 | `GET/POST P/metrics`，列表必填环境 |
| 资源/草稿 | `GET M`，`GET/PATCH M/draft` |
| 校验 | `POST M/validations`，返回 diff 和绑定范围/基线的摘要，不执行查询 |
| 版本 | `GET/POST M/versions`，`GET M/versions/{version}` |
| 执行 | `POST M/versions/{version}/queries`，返回普通异步查询任务 |

关键不变量：

- 发布固定当前项目/环境中的已发布事件版本，每个事件暂限一个版本；事务内重算依赖与摘要。
- 草稿 PATCH 使用 `If-Match`；创建/发布/运行使用明确的幂等键。创建和发布重试返回原快照，不能当最新状态继续写。
- 运行省略 `interval` 是整段聚合；显式 `minute/hour/day` 才分桶。不能相加每日 UV 当整段 UV。
- 固定筛选与运行筛选以 AND 组合；合并后的条件树仍受总预算限制。
- `metrics.read/write/publish/run` 分别授权；校验需要 `metrics.write`。运行另需 `queries.run`，读取结果需 `queries.read`，身份字段可能另需 `queries.raw.read`。
- 原 `analyses.*` 权限不授予新指标能力。迁移只扩充授权白名单，不改已有机器主体权限。
- 执行重试标记保留 7 天；有效标记对应的任务过期/删除会返回 `QUERY_REQUEST_EXPIRED`，新执行需新键。
- 版本、事件依赖、审计、指针和幂等记录在事务边界内保存，不能拆成先写部分再异步补齐。

### 5.2 项目治理，迁移 011

阅读 [项目治理](docs/project-governance.md) 和 [账号运维](docs/runbooks/accounts.md)。

入口是 `backend/internal/access/{members,settings,audit,provision}.go`、对应 PostgreSQL 文件、`backend/internal/transport/control/governance*.go` 和 `tooling/verification/governance_smoke.py`。

这些接口只接受人类会话，不接受机器 Bearer。成员管理和审计读取要求 owner；所有当前成员可读设置，只有 owner 可写。成员/设置写入均需 CSRF、`If-Match` 和 `Idempotency-Key`。

成员修订属于整个成员集合，独立于设置修订。必须保留最后一个启用 owner；删除再添加不复用旧修订。并发锁顺序、撤权后重试拒绝和查询授权复查已有真实数据库测试，不要绕过事务来简化 UI。

`account-create` 只创建新账号与独立运维审计，不创建会话或成员关系，不覆盖现有密码，也不是账号恢复功能。它使用数据库运维权限和私有环境输入，不应包装成普通项目 owner 可调用的全局账号创建 API。`account-disable`/`account-enable` 同属运维命令，切换 `accounts.disabled` 并禁止停用最后一个启用账号，仅在真实状态转换时向 `account_provision_events` 写 `account_disabled`/`account_enabled`，幂等 no-op 不写。`account-reset-password` 替换密码哈希并在同事务删除该账号全部会话、写 `password_reset`，是账号恢复路径。`account-audit` 是只读命令，按用户名读回该账号的类型化审计事件（有序 event_type+UTC 时间，未知账号 ACCOUNT_NOT_FOUND，不回显凭据），让迁移 028 的类型化审计形成读写闭环。迁移 028 起该表 `event_type` 为四值闭集且无默认值，所有审计行与状态/凭据变更同事务原子提交。

### 5.3 保存分析与看板

阅读 [保存分析](docs/saved-analyses.md)、[看板](docs/dashboards.md) 和 [前端模块职责](docs/standards/frontend-modules.md)。

现有依赖关系是：已发布事件版本 → 保存分析版本 → 看板版本。独立指标是旁路的可执行配置资源，尚未插入这条关系。扩展指标引用时必须兼容已有不可变分析/看板版本及其哈希，不能批量改写历史 JSON 或在运行时隐式取最新指标。

看板原子创建子任务不表示所有子查询观察到同一数据库快照。各图保留自己的生成时间、水位、精度、覆盖和错误，不得用一个成功图替整个看板掩盖失败。

## 6. SDK 与业务接入

入口为 `packages/browser-sdk`，公开函数是 `createEviaClient`；方法包括 `init`、`emit`、`identify`、`beginNavigation`、`navigate`、`flush`、`reset`、`disable/enable` 和 `destroy`。完整语义见 [SDK 文档](docs/browser-sdk.md)。

必须保留这些边界：

- `emit` 立即捕获事件、身份、页面和业务属性快照，异步发送；重试不改写原事件字节或升级成新的发布版本。
- `identify(uid)` 只影响未来事件，没有历史匿名事件合并；`reset()` 更换后续匿名/会话/页面范围，已排队事件不变。
- `navigate()` 只更新导航上下文，不自动发送页面事件。业务方必须显式发送已注册的页面事件。
- 单实例 SPA 导航可关联；刷新/跨文档会重新创建 tab/order/session/page 范围，当前只持久化匿名 UUID。
- 默认队列最多 200 个事件、256 KiB；默认批次最多 20 条、60 KiB，单请求在途、有界刷新和重试。具体限制以当前实现为准。
- `flush()` 返回有界本轮后的 pending 数，不保证所有事件已送达。要看每个 emission 的 result；`accepted` 只代表持久队列确认，不能当 ClickHouse 可查询证明。
- 当前没有自动 DOM 采集、URL/参数抓取或持久离线队列；五个 sys.* 事件已从 SDK 产生、公开配置下发、采集器旁路到 Kafka/ClickHouse 落库全链路打通，公开 emit 仍禁止伪造保留键。

平台不为每个网站维护专用页面适配层。业务方的 coding agent 使用公开 `activity-event-plan`、`activity-publish-events`、`activity-integrate-verify`，在自己的仓库梳理触发点、注册/发布事件、安装 SDK 并验证实际入库。

维护 skill 位于 `.agents/skills/platform-*`；公开 skill 位于 `integrations/skills/activity-*`。公开打包工具 `integrations/tooling/package_assets.py` 只允许 9 个明确文件，不应收集整仓指令。`activity_api.py` 每次请求先发现机器身份，再核对项目/环境；拒绝重定向，响应只写新的私有文件。新增指标路径已在其 allowlist 内；它仍不是全自动业务接入服务。

实际目标业务仓库的接入验收未完成。已通过的合成独立网页跨域验收不能表述成某个真实业务网站已经完成接入。

## 7. 必须保持的工程约束

### 契约与服务端

- 权威定义在 `contracts/`。修改后运行生成器，不手改 Go/TypeScript 生成文件。领域模型不依赖 HTTP 或数据库，存储模型不直接用生成 wire 类型。
- 管理/查询路径 `/api/v1`，公开配置/采集路径 `/v1`；wire 字段 snake_case，ID 为字符串，时间为 UTC RFC3339，数值遵守安全范围。
- 资源成功直接返回资源，列表为 `{items,next_cursor}`，错误为 `{code,message,request_id,details?}`。不要再套一层不一致的 `data/status`。
- 严格拒绝重复 JSON key、未知字段、尾随值、歧义请求头和非法查询参数；限制正文、深度、数组、时间范围和查询量。
- 游标必须绑定操作者、项目及相应环境/资源/过滤；签名游标不自动等于跨页一致快照。
- 认证和实际环境授权在事务内重查，且在幂等重放之前进行；不能信任客户端传来的资源范围或旧权限缓存。
- 人类会话、管理机器凭据、浏览器写 key、服务端采集凭据是不同边界。混合 Cookie/Bearer 不回退，来源凭据不访问管理 API。
- 机器查询保存原始凭据，领取、租约和完成时复查。轮换后的新凭据不能为旧任务续权；已完成结果按当前读取者权限判断。
- 使用参数化 SQL、显式事务、context 取消和有界操作。新 migration 向前追加，不改已执行文件；`serve` 不自动迁移。
- 用户身份/授权变更、任务完成与业务事务的锁顺序有并发测试。不要在持有项目锁后引入相反的成员/任务锁等待。

### 数据与查询语义

- 接收回执、Kafka 确认、ClickHouse 可见、去重事实、查询成功是不同阶段。
- 去重身份包含来源和事件标识，并受项目/环境约束；优化候选筛选不能丢掉候选事件的其他重放版本或改变胜出规则。
- `event_versions` 才固定事实版本；属性依赖摘要本身不代表查询事实已经固定。历史 `event_keys` 查询语义不要暗改。
- UID/匿名标识不是已解析的人；`resolved_person` 和身份修订当前拒绝。
- 缺失、null、false、零、空字符串及数组需要保留区别。不要使用不等价属性投影换取更快的测试数字。
- 当前结果覆盖是 `provisional`，水位是实际观察到的最晚接收时间，不是全分区完整性承诺。
- 查询共享项目预算：最多 4 个运行、32 个排队；trend 最多 31 天，其他查询最多 7 天；最多 20 个事件选择、3 个分组、10 个聚合和 500 行，当前没有结果续页。新增模块不能绕过这些预算。

### 前端与状态

- 先读 [UI 规范](docs/standards/ui.md) 和对应 scoped `AGENTS.md`。普通控件在 `packages/design-system` 中复用。
- 包依赖方向必须保持；设计系统不依赖 API 业务类型，API 客户端不依赖 React，SDK 不依赖管理客户端。
- 本地编辑草稿、已保存修订、校验结果、已提交不可变版本和运行结果分别管理。异步响应必须仍属于当前会话/项目/环境才能更新 UI。
- 未知提交结果保留原请求及幂等键；失败不清空用户输入；旧请求完成不能覆盖新选择或抢走新焦点。
- 图表展示全部返回指标/分组，保留表格、单位、时间/时区及覆盖信息；不能用当前正在编辑的草稿重新解释旧结果。
- 自动截图/trace/video 在密钥流程中保持关闭；只保存人工核对过的无密钥截图。

## 8. 真实验证证据与失败基线

最近功能基线的 [CI](https://github.com/howtomakeaname/Evia/actions/runs/42625615892) 验证的是完整 SHA `a8c0f569ab8cb4de4bc10e4d24853e5247cab331`，四项任务均成功：主检查、Redis 配额、Kafka/ClickHouse 管道、ClickHouse 查询。

| 证据 | 结果与解释 |
| --- | --- |
| 完整后端 | gofmt、全量 vet、`go test -race ./...` 通过 |
| 完整 PostgreSQL | 本地竞态集成 127.543 秒通过，本批新增 16 个顶层指标集成测试及子测试 |
| 指标真实链 | 本地 139 次 HTTP、4 个查询；CI 136 次 HTTP、4 个查询，轮询次数不同；两者均通过 |
| 合成事实 | 6 次提交含 1 次重复，核对为 5 条事实、3 个原始 UID；固定条件 count=3、UV=2，冲突运行条件 count=0 |
| 治理真实链 | 44 次 HTTP、3 个合成账号，已纳入 CI |
| 浏览器 | 本地生产构建 45/45，1.6 分钟；同一提交 CI 45/45，1.4 分钟，无跳过 |
| 客户端/公开资产 | 16 个客户端测试、14 个公开资产测试通过；真实公共 API 工具读取已验证 |
| SDK | 已有 20 项测试、源码打包及独立网站真实跨域链证据，尚未公开发行 |
| 截图 | [指标授权](docs/assets/metric-grants-desktop.png)、看板及机器详情截图见各模块记录 |

最新详细证据见 [指标](docs/verification/metrics-2026-11-09.md)、[治理](docs/verification/project-governance-2026-11-09.md)、[机器访问](docs/verification/machine-access-2026-11-09.md)、[看板与 SDK](docs/verification/dashboards-sdk-2026-11-09.md)。报告中旧提交、旧浏览器用例数和迁移版本是当时的快照，不要全部改成最新数字。

### 容量问题尚未解决

已有真实十万/百万事件上报证据，但当前不能声称支撑千万用户或生产就绪。

- 十万数据八类查询矩阵曾达到 **728/728**。
- 百万数据、默认预算与双工作任务的完整矩阵仍为 **514/728**，不是全通过。
- 指定 UID 筛选、原始行为链、有序漏斗合计 **273/273** 通过。
- 两类属性查询仍有 **182** 次失败，计数/UV/小时分桶另有 **32** 次并发资源拒绝。
- 单工作任务另做的 **27/27** 是不同配置的小样本，端到端 p95 约 31–42 秒，包含排队，不能替代完整矩阵。
- 属性列裁剪原型曾因数字/数组语义不等价而未交付。紧凑列、身份投影和固定范围聚合优化已经实现，下一位不要重复“首次实现”或直接恢复错误原型。

读取 [百万查询报告](docs/verification/query-capacity-2026-11-09.md)、[上报压测报告](docs/verification/load-and-maintainability-2026-11-08.md) 和 [压测运行手册](docs/runbooks/benchmark.md)。固定数据分布、资源、查询集合和并发做对比；保留失败、排队、读行/字节、峰值内存和成功样本分位数。生产 HA、长保留期、长尾、多租户公平性、持续混合读写和故障恢复仍需独立验证。

## 9. 建议下一步及验收条件

建议把功能扩展与容量修复作为两条可并行主线。以下是续开发建议，具体顺序服从用户的新指令。

| 工作包 | 推荐入口与实现要求 | 可检查的完成条件 |
| --- | --- | --- |
| A. 保存分析引用指标版本 | reporting、契约、PostgreSQL 依赖；先确定兼容模型，再增加明确的 metric_id/version 引用 | 旧分析/看板仍可运行，旧版不可变；改指标不隐式改旧图；跨环境/权限/依赖类型拒绝；验证 diff、幂等、并发与真实结果 |
| B. 大范围属性查询与共享内存 | analysis 的 `trend_facts.go`、`planner.go`、字段解析及查询准入 | 同条件完整百万矩阵有可解释改善，语义对照通过，不靠放宽预算/只报成功样本；持续混合读写另验 |
| C. 指标公式 | 受限有类型 AST、固定依赖 DAG、单位/精度/null/除零语义、总展开预算 | 无任意 SQL/JS；循环/过深/越权拒绝；除零/缺失不伪装为零；同时间/主体/维度可解释地对齐 |
| D. 治理与指标控制台 | 先补共用客户端方法，再用已有组件实现成员、设置、审计、指标编辑/发布/运行 | 真实 API 全链、完整交互/错误/冲突/未知结果恢复、桌面/移动截图；不靠静态空壳 |
| E. SDK 连续行为链与身份关系 | 先定义跨文档 session/tab/navigation、登录/退出、匿名关系及冲突规则 | 硬导航/刷新/多标签、用户切换、旧队列快照、版本化解析、撤销/删除与恢复均有明确证据 |
| F. 生命周期与导出 | 保留/删除/归档/回补、恢复后的删除屏障、有界导出任务与下载授权 | 已删除数据不因恢复复活；失败恢复、取消、过期、下载撤权与审计可验证 |
| G. 高级分析与生产工程 | 留存/分群/聚合路径/归因等各自语义，生产安装/监控/备份/恢复 | 对应计算模型和预算真实实现，生产拓扑与恢复演练单独验收 |

不要把独立原子指标改名当公式，把原始 journey 改名当聚合路径，把单节点压测成功当 HA，或把已完成后端隐藏在未接入的 UI 里后宣称整模块交付。

## 10. 环境准备与验证命令

### 新机器上的可复现路径

1. 拉取仓库，核对工作树和远端；已有变更时先保留、辨认，不做强制 reset。
2. 安装仓库锁定的 Go/Node/pnpm、Python 验证依赖及浏览器。细节见 [本地开发](docs/runbooks/development.md)。Go 竞态测试需要支持 CGO 的 C 工具链。
3. 为开发/测试创建专用 PostgreSQL 数据库，私有配置保留在仓库外或忽略目录，权限 `0600`。
4. 按 [依赖手册](docs/runbooks/dependencies.md) 启动需要的 Kafka、ClickHouse、Redis；按 [完整控制台验证](docs/runbooks/console-verification.md) 生成完整运行配置、准备主题和 ClickHouse 迁移。
5. 用同一新二进制显式迁移 PostgreSQL 至当前 `012`；新空库才 bootstrap。已有账号不得靠重复 bootstrap 重置。
6. 分别启动 `serve`、`ingest-worker`、`query-worker`。配置探针后，API 同时运行交付协调。仅启动 PostgreSQL/API 不足以验证完整平台。
7. 等待 `http://127.0.0.1:8080/readyz` 返回 204；控制台使用配置中的精确 Origin `http://127.0.0.1:5173`。

下面用 `/private/evia-runtime.env` 表示已经按运行手册生成的私有配置，不是可直接复制的现成文件：

```bash
pnpm install --frozen-lockfile
mkdir -p bin
go -C backend build -o "$PWD/bin/evia" ./cmd/evia
python3 tooling/verification/browser_runtime.py run /private/evia-runtime.env -- ./bin/evia migrate
# 仅新建空账号库需要下面这一步：
python3 tooling/verification/browser_runtime.py run /private/evia-runtime.env -- ./bin/evia bootstrap

# 分别在三个终端启动，保留相同配置和同一二进制：
python3 tooling/verification/browser_runtime.py run /private/evia-runtime.env -- ./bin/evia serve
python3 tooling/verification/browser_runtime.py run /private/evia-runtime.env -- ./bin/evia ingest-worker
python3 tooling/verification/browser_runtime.py run /private/evia-runtime.env -- ./bin/evia query-worker
```

核心检查从仓库根运行，按变更范围选择；不要为了文档修改重复所有运行测试：

```bash
make generated-check
make check
make integration-test
make pipeline-test
make query-test
make budget-test
python3 tooling/verification/browser_runtime.py run /private/evia-runtime.env -- make browser-test
python3 tooling/verification/browser_runtime.py run /private/evia-runtime.env -- \
  python3 tooling/verification/governance_smoke.py --binary "$PWD/bin/evia"
python3 tooling/verification/browser_runtime.py run /private/evia-runtime.env -- \
  python3 tooling/verification/metrics_smoke.py --binary "$PWD/bin/evia"
```

`integration-test` 要求 `EVIA_TEST_DATABASE_URL`，pipeline/query/budget 需要对应 `EVIA_TEST_KAFKA_*`、`EVIA_TEST_CLICKHOUSE_*`、`EVIA_TEST_REDIS_URL`。用 `deploy/dev-env run` 注入依赖配置，不能把功能运行变量与集成测试变量想当然地互换。完整浏览器运行需要 `EVIA_E2E_FULL_STACK=1` 及私有测试账号；否则部分用例会跳过。

Go/TS 生成与读取生成文件的格式/编译检查顺序执行，避免一个进程重写文件时另一个检查瞬时失败。契约的预期修改必须连同生成文件一起审查，不能靠手改生成文件满足检查。

首次完整基线验证可参考 CI `.github/workflows/verify.yaml`，它是执行流程，不是成功证据。通过状态必须绑定真实提交和 run URL。

### 若继承本次云工作区

下列信息仅用于复用当前机器，不属于仓库安装要求，也不包含凭据。新环境不得假定这些文件或进程存在。

| 项目 | 本次工作区信息 |
| --- | --- |
| 仓库目录 | `/workspace/Evia` |
| Go / gofmt | `/workspace/.tools/evia/go-env`，`/workspace/.tools/evia/go/bin/gofmt` |
| pnpm | `/workspace/.tools/evia/pnpm-env` |
| Python 验证环境 | `/tmp/evia-api-verify-venv/bin/python` |
| Chromium 安装位置 | `PLAYWRIGHT_BROWSERS_PATH=/workspace/.cache/evia/playwright` |
| 私有功能配置 | `/tmp/evia-browser-runtime.env`，仅列位置，不复制内容 |
| 私有 PostgreSQL 测试配置 | `/tmp/evia-test.env` |
| 当前功能二进制 | `/tmp/evia-metrics-runtime` |
| 功能运行状态 | 写交接时三个子命令仍运行，API 8080 的 readiness 实测为 204 |
| 当前控制台 | `/workspace/evia-metrics-release` 的生产构建，5173 预览，入口 `/assets/index-CFt0DjHa.js` |
| 组件夹具端口 | 5174，Playwright 按需启动，不是业务站点 |
| 功能依赖端口 | PostgreSQL 55432，Kafka 19092，ClickHouse HTTP 18123/native 19000，Redis 16379 |
| 另一个压测环境 | 使用独立配置/数据/端口，例如 API 8081；状态需重新核对，不得覆盖功能或压测数据 |

该二进制 SHA-256 为 `be7fdcb37054f7af7f20633f53ebf84ed4b7f1fdc19971e8f47f3927feaede76`。其构建时工作树未提交，功能源码后来提交于 `ac81e3e`；不要把二进制内的旧 VCS 元数据直接当最新源码版本。

本机可这样复用已有工具和私有配置：

```bash
make backend-test GO=/workspace/.tools/evia/go-env GOFMT=/workspace/.tools/evia/go/bin/gofmt
python3 tooling/verification/browser_runtime.py run /tmp/evia-test.env -- \
  make integration-test GO=/workspace/.tools/evia/go-env
PLAYWRIGHT_BROWSERS_PATH=/workspace/.cache/evia/playwright \
  python3 tooling/verification/browser_runtime.py run /tmp/evia-browser-runtime.env -- \
  /workspace/.tools/evia/pnpm-env test:browser
```

`browser_runtime.load()` 返回 `(Path, dict)`，不是 dict；`run` 会切换到仓库根。调用 Go package 命令时使用 `go -C backend` 或正确绝对工作目录。复用的预览服务可能仍指向旧构建，必须核对实际 entry asset；仅重新 build 不会自动切换冻结目录。

### 环境与凭据注意事项

- 不输出私有 env、Cookie、token、数据库 URL 中的密码或一次性密钥；不用凭据值作为命令行参数。
- 继承 Cloud 环境时先按其 runtime skill 核对环境状态；保留代理、CA、TLS 校验和 `NO_PROXY`。
- 初始化脚本拒绝覆盖现有配置；修改 env 密码不会自动修改持久数据库中的账号密码。
- 不随意销毁共享依赖、卷或压测数据，也不使用 Redis 全库清空来修测试。
- 登录预算保持开启；重复浏览器调试使用专用账号或正常冷却，不能关闭限流换取通过。
- `.env`、运行日志、未审查 trace、完整 API 响应和本机临时文件不进入 Git。测试只用合成数据。

## 11. 推荐的接力工作方式

1. 先核对 `master` 与工作区，阅读本文、标准及最新覆盖清单，选定一个有明确验收条件的工作包。
2. 用简短说明告诉用户本批目标；不重复询问已有的仓库、技术栈、Redis、自建 UI 或直接推送偏好。
3. 列出将改的契约、模块、迁移和证据。并行前锁定共享接口，分配文件所有权；一个 agent 负责生成，一个负责迁移编号。
4. 先完成后端与真实依赖证据，再接同模块前端；其他互不依赖后端可并行。前端使用已有设计系统和共用客户端。
5. 审查权限、事务、重试、版本、容量与失败路径；代码检查、真实 DB、浏览器和压测各自报告，不互相替代。
6. 小步 commit，更新 PROGRESS/README/覆盖清单/模块文档，推送；记录真实 CI 状态。只补跑失败且有理由的任务，不反复跑已通过检查。
7. 把“本批交付”与“全产品完成”区分清楚，向用户给出提交、实际验证、重要剩余项和必要截图。

可以把下面这段作为下一位 agent 的启动说明：

> 继续开发 Evia，仓库为 howtomakeaname/Evia。先阅读根目录 HANDOFF.md、AGENTS.md、PROGRESS.md 和 docs/feature-coverage.md，再核对最新 master。沿用 Go 单模块、PostgreSQL/Kafka/ClickHouse/Redis 和 React 自建组件方案，按后端、真实验证、前端的顺序补齐功能。优先衔接独立指标的版本引用与当前性能缺口，不重写已有模块，不把规划当实现。保持契约、权限、不可变版本、幂等和现有交互行为，小步提交并推送，持续更新进度。普通实现自主推进，实质不确定事项再沟通。

## 12. 文档索引

| 主题 | 入口 |
| --- | --- |
| 总体状态与历史证据 | [README](README.md)、[PROGRESS](PROGRESS.md)、[覆盖清单](docs/feature-coverage.md) |
| 架构与长期关系 | [架构](docs/architecture.md)、[看板方案](docs/plans/custom-dashboards.md) |
| 工程规范 | [API](docs/standards/api.md)、[开发/验证](docs/standards/development.md)、[UI](docs/standards/ui.md)、[前端模块](docs/standards/frontend-modules.md) |
| 内部 skills | [API](.agents/skills/platform-api/SKILL.md)、[UI](.agents/skills/platform-ui/SKILL.md)、[验证](.agents/skills/platform-verification/SKILL.md) |
| 业务模块 | [发布](docs/releases.md)、[查询](docs/analysis.md)、[发现](docs/reporting-discovery.md)、[保存分析](docs/saved-analyses.md)、[看板](docs/dashboards.md)、[指标](docs/metrics.md) |
| 身份与权限 | [机器访问](docs/machine-access.md)、[治理](docs/project-governance.md)、[账号运维](docs/runbooks/accounts.md) |
| SDK 与外部接入 | [SDK](docs/browser-sdk.md)、[公开资产](integrations/README.md) |
| 环境与验证 | [开发](docs/runbooks/development.md)、[依赖](docs/runbooks/dependencies.md)、[控制台](docs/runbooks/console-verification.md)、[HTTP 验收](docs/runbooks/backend-smoke.md) |
| 压测与升级 | [压测](docs/runbooks/benchmark.md)、[紧凑列升级](docs/runbooks/compact-winner-upgrade.md)、[身份查询升级](docs/runbooks/identity-query-upgrade.md)、[部署边界](docs/runbooks/deployment.md) |
| 高可用与恢复 | [优雅排空与多副本](docs/runbooks/ha.md)、[备份恢复](docs/runbooks/backup-restore.md)、[SLO 与告警](docs/runbooks/slo.md)、[观测性](docs/runbooks/observability.md) |
| 图示与截图 | [资产来源与维护](docs/assets/README.md)、[最新指标验证](docs/verification/metrics-2026-11-09.md) |
