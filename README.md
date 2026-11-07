# Evia 映途

私有化事件采集与用户行为分析平台。以业务事件定义、稳定用户身份和可解释的分析口径，把页面访问、阅读和互动连接起来。

当前优先完善服务端，按可独立审查的小提交交付。已实现会话与项目管理、事件定义和草稿校验、不可变发布记录；采集处理与查询分析正在开发。实际完成项与验证证据以 [PROGRESS.md](PROGRESS.md) 为准，尚不具备整个平台的生产交付条件。

## 启动与验证

使用 Go 1.27.3、Node 24.22.1、pnpm 11.25.0 和 PostgreSQL 17.9。按 [本地开发说明](docs/runbooks/development.md) 生成本地配置，显式执行数据库迁移及初始账号创建，再启动 API 和控制台。

```sh
make generated-check
make check
make integration-test
make browser-test
```

集成测试和浏览器测试需要说明中配置的真实依赖及专用测试账号。缺少依赖时测试应失败，不会自动改用模拟数据。部署边界见 [运维说明](docs/runbooks/deployment.md)。

## 技术方向

Go 服务端、React 管理控制台、TypeScript SDK；PostgreSQL 管理元数据，Kafka 缓冲事件，ClickHouse 支撑分析，Redis 提供分布式接入配额。有状态的身份与会话处理单独规划。业务方通过公开 API、SDK 和接入 skill，在自己的仓库维护网站专属代码。

发布记录已保存，不代表事件接收已就绪。当前发布记录明确返回 `ingestion_ready: false`，接收启用必须由后续运行时确认。千万用户是架构目标，尚未进行对应硬件、流量分布和故障条件下的容量验证。

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

控制台已有经过浏览器验证的基础版本。后续将按统一界面规范迁移为自建基础组件；当前版本仍使用 Ant Design。浏览器 SDK、业务方公开接入 skills 和分析页面尚未交付，不能用现有控制台状态替代这些能力的实现。

## 工程规范

- [接口规范](docs/standards/api.md)
- [测试与提交规范](docs/standards/development.md)
- [界面规范](docs/standards/ui.md)
- [架构与范围](docs/architecture.md)

使用者执行代码前应阅读对应阶段的启动说明。仓库不包含真实业务数据或可用的生产凭据。
