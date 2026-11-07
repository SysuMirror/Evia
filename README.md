# Evia · 映途

私有化事件采集与用户行为分析平台。以业务事件定义、稳定用户身份和可解释的分析口径，把页面访问、阅读和互动连接起来。

当前正在按模块开发，尚不是可用于生产的完整平台。实际完成项与验证证据以 [PROGRESS.md](PROGRESS.md) 为准。

## 技术方向

Go 服务端、React 管理控制台、TypeScript SDK；PostgreSQL 管理元数据，Kafka 缓冲事件，Flink 处理有状态数据，ClickHouse 支撑分析。业务方通过公开 API、SDK 和接入 skill，在自己的仓库维护网站专属代码。

## 工程规范

- [接口规范](docs/standards/api.md)
- [测试与提交规范](docs/standards/development.md)
- [界面规范](docs/standards/ui.md)
- [架构与范围](docs/architecture.md)

使用者执行代码前应阅读对应阶段的启动说明。仓库不包含真实业务数据或可用的生产凭据。
