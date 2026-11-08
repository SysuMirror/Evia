# Evia 映途

私有化事件采集与用户行为分析平台。以业务事件定义、稳定用户身份和可解释的分析口径，把页面访问、阅读和互动连接起来。

当前优先完善服务端，按可独立审查的小提交交付。已实现会话与项目管理、事件定义、来源管理、接收策略、Redis 配额、Kafka 到 ClickHouse 写入、有界查询，以及真实管道验证后的发布激活和公开配置。

控制台已提供项目与环境工作区：编辑和验证事件字段、发布并显式激活版本、管理浏览器与服务端来源，以及运行指标、趋势、事件明细、用户旅程和有序漏斗查询。PV 可选择已发布的自定义页面事件，属性筛选使用不可变版本中的真实字段类型。完整浏览器回归 **19 项通过**，详见 [验证记录](docs/verification/core-console-2026-11-08.md) 和 [启动说明](docs/runbooks/console-verification.md)。

控制台进一步调整了共享组件、资源表单和分析工作台：结果前置，复杂条件独立编辑，补齐焦点、选中、错误恢复和通知交互。构建、截图和本轮复验见 [界面交互记录](docs/verification/console-experience-2026-11-08.md)。

服务端新增 [分析发现基础](docs/reporting-discovery.md)：显式 `event_versions` 固定真实查询事实，提供受权能力/已发布字段目录及无执行查询校验。已通过真实 PostgreSQL/ClickHouse 与处理器测试，[本轮证据](docs/verification/reporting-foundation-2026-11-08.md) 未包含新接口的运行中 HTTP 或浏览器验收。保存分析、看板和机器令牌仍待实现。

冻结快照 `8d2d9d5` 已通过真实 PostgreSQL、Redis、Kafka、ClickHouse 与独立工作进程组成的公开 HTTP 链路验收：发布、就绪、激活、接收重试、查询和来源停用均通过。自定义页面事件得到计数 **3**、精确 UID 去重数 **2**、指定 UID 行为链中的来源/事件标识对 **2**；详见 [2026-11-07 验证记录](docs/verification/backend-2026-11-07.md)。完整进度见 [PROGRESS.md](PROGRESS.md)，整个平台仍未达到生产交付条件。

## 架构概览

![Evia 数据架构：控制台经 Go API 使用 PostgreSQL，事件经接收 API、Kafka 和写入工作进程进入 ClickHouse，Redis 提供接入预算，查询工作进程消费 PostgreSQL 任务并读取 ClickHouse。浏览器 SDK、身份处理和自定义看板为规划模块。](docs/assets/event-data-architecture.png)

图 1，平台逻辑链路。实线框表示已实现模块，虚线框表示规划模块；重复出现的数据库表示同一存储职责，图中分层不代表独立部署实例。容量目标及验证边界见 [架构说明](docs/architecture.md) 和 [开发进度](PROGRESS.md)。

## 启动与验证

使用 Go 1.27.3、Node 24.22.1、pnpm 11.25.0 和 PostgreSQL 17.9。按 [本地开发说明](docs/runbooks/development.md) 生成本地配置，显式执行数据库迁移及初始账号创建，再启动 API 和控制台。

服务端链路测试另外使用 Kafka 4.4.0、ClickHouse 26.8.36.3 和 Redis 8.10.3，安装与私有配置见 [依赖说明](docs/runbooks/dependencies.md)。

```sh
make generated-check
make check
make integration-test
make pipeline-test
make budget-test
make query-test
make browser-test
```

集成测试和浏览器测试需要说明中配置的真实依赖及专用测试账号。完整服务端 HTTP 验收还需显式迁移及启动 API、接收工作进程和查询工作进程，按 [服务端验收说明](docs/runbooks/backend-smoke.md) 执行。缺少依赖时测试失败。部署边界见 [运维说明](docs/runbooks/deployment.md)。

## 技术方向

Go 服务端、React 管理控制台；PostgreSQL 管理元数据，Kafka 缓冲事件，ClickHouse 支撑分析，Redis 提供分布式接入配额。有状态的身份图与 Flink 处理单独规划。后续 TypeScript 浏览器 SDK 和公开接入 skill 将支持业务方在自己的仓库维护网站专属代码。

发布定义保存后先等待交付验证：带项目、环境和版本范围的探针经过真实 Kafka 写入及 ClickHouse 回读，形成有期限的就绪租约；随后通过显式版本比较和幂等键激活。`ingestion_ready` 按有效租约派生，租约过期会失去就绪资格。公开配置先验证来源与发布资格，再处理 ETag。千万用户仍是架构目标，尚未进行对应硬件、流量分布和故障条件下的容量验证。

![Evia 版本化分析模型：现有事件定义经过草稿、校验、不可变发布、管道验证和显式激活；规划中的指标版本、保存分析和看板版本依赖已发布事件字段，控制台与 Agent 共用报告 API。](docs/assets/versioned-analytics-model.png)

图 2，事件发布流程与规划中的分析依赖。事件发布不等于允许接收；本轮已实现查询版本选择与发现/校验接口；指标、保存分析、看板及其配置 API 仍待实现，版本固定与显式升级规则见 [自定义看板方案](docs/plans/custom-dashboards.md)。两图由 ImageGen 生成并按仓库现状校对，[维护说明](docs/assets/README.md) 记录了语义来源。

## 仓库布局

| 目录 | 职责 |
| --- | --- |
| `backend/` | 单一 Go 模块，应用策略、HTTP 边界、持久化适配分别组织 |
| `contracts/` | OpenAPI、JSON Schema 与合成数据契约样例 |
| `apps/console/` | 控制台业务页面 |
| `packages/design-system/` | 共享主题、布局和交互组件 |
| `packages/api-client/` | 生成的接口类型及统一请求处理 |
| `.agents/skills/` | 平台内部开发规范 |
| `deploy/`、`tooling/` | 本地依赖、迁移、生成与验证工具 |

控制台采用 Evia 自建 React/原生 HTML 组件，Ant Design 及其依赖已移除。共享组件覆盖键盘和鼠标状态、表单校验、焦点恢复、弹窗、通知、Tab 与未保存内容保护。专业趋势和漏斗图按需加载 ECharts，并提供对应结果表；普通页面不会加载图表渲染模块。此前基础组件迁移的体积比较见 [前端验证记录](docs/verification/native-ui-2026-11-07.md)，不能将该比例当作新增业务功能后的总体体积结论。

自定义指标、保存分析、看板配置及供 coding agent 使用的同一套公开 API 已形成 [设计方案](docs/plans/custom-dashboards.md)。目前实现了会话认证的发现/校验基础与真实查询版本选择，保存配置、看板协调和机器认证运行时尚未实现。浏览器 SDK、`sys.*` 内置事件运行时注册、身份关系图、成员管理、数据生命周期、报表看板导出及公开接入 skills/CLI 仍待交付。现有分析工作区不等于可保存、布局和发布的自定义看板；`sys.*` 当前保留契约样例，页面事件计数使用显式发布的自定义事件。

## 工程规范

- [接口规范](docs/standards/api.md)
- [测试与提交规范](docs/standards/development.md)
- [界面规范](docs/standards/ui.md)
- [架构与范围](docs/architecture.md)

使用者执行代码前应阅读对应阶段的启动说明。仓库不包含真实业务数据或可用的生产凭据。
