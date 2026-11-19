# 身份图切片①验证记录（2026-11-20）

## 交付范围

按 [身份图规划](../plans/identity-graph.md) 切片①交付图构建只读闭环：迁移 043 五表、`internal/identity` 领域与纯折叠、`evia identity-worker`、`pipeline.IdentityLinkScanner`、`control.IdentityAPI` 三路由、OpenAPI 契约与 codegen、Prometheus 指标。查询侧 `resolved_person`/`identity_revision` 拒绝保持原样（切片②才放开）。运行手册与语义见 [docs/identity.md](../identity.md)。

## 验证

### 领域与 worker 单测（`go test ./internal/identity/`，13 例）

合并存活规则（古老分量存活、平局按 id）、成员整体迁移保留首见/加入修订、纯重叠重扫零变更、边推进保留首见、不完整候选跳过、空扫描 no-op；worker：提交路径、屏障失败 fail-closed 释放、扫描失败 fail-closed 释放、重叠重扫不产修订、tombstone 身份过滤、租约被持有时静默跳过。

**测试抓到的真实缺陷**：PostgreSQL `timestamptz` 微秒精度与 ClickHouse `DateTime64(9)` 纳秒残差会让同一对的重扫被误判为「推进」从而刷出虚假修订；修复为领域入口统一 `Truncate(time.Microsecond)` 并以集成测试钉住。

### 真实 PostgreSQL 集成（`-race -tags=integration`，5 例）

端到端：作用域发现 → 认领 → 建图（2 分量）→ 重扫幂等（不产修订）→ 桥接对合并（redirect + 成员迁移）→ 列表/详情/resolve（redirect 已解析、旧钉版 `IDENTITY_REVISION_NOT_RESOLVABLE`）→ 水位断言；租约互斥与释放后重认领、过期租约提交被守卫拒绝；failed 修订记录错误码且不推水位；权限边界（viewer 可读修订、resolve 需 owner/editor、外人 `PROJECT_NOT_FOUND`）；真实 `erasure_tombstones` 表屏障过滤（经 worker 全链路，被删身份不建边）。整套 `make integration-test` 全量 `-race` 通过（EXIT=0）。

### 真实 ClickHouse 集成（`-race -tags=integration`，1 例）

合成 `sys.identity_link` 事实（同一对两次 + 缺 uid 的不完整链接 + 无关事件 + 事实迟到）：聚合对返回 min/max 接收时间、不完整与无关行被排除、水位分区排除已扫、环境隔离、limit 分批续读标志。`make backend-test` 全量 `-race` 通过；`make generated-check` 在提交后一致（契约与生成物同批提交）。

### 实机全栈链路（真实 serve/identity-worker + PG/CH）

`evia-fullstack` 环境迁移至 043 后，以新二进制运行 `serve` 与 `identity-worker`；Playwright 驱动合成站点经真实 collect API 产生 `sys.identity_link`（两设备共享账号一、账号二再共享设备二 → 一个连通分量），worker 首轮折叠后 `GET /identity-revisions` 返回 completed 修订、`resolve?uid=demo-user-1` 返回同一 person 的全部 4 个成员（2 uid + 2 匿名设备）。无凭据截图：`identity-revisions-api.png`、`identity-resolve-api.png`（本地工件，不进仓库）。

期间修复的装配缺陷：`config.ReadFor` 与命令白名单未包含 `identity-worker`（ClickHouse 配置不加载导致启动探测失败）——补入两处白名单并保留一条启动探测失败的具体错误日志。

## 边界（不声称）

- resolve 仅支持最新 completed 修订；历史成员快照未持久化（规划文档已修订记录）。
- 查询集成（resolved_person 展开、成员预算 1024、两个新错误码）、erasure 联动退役、回补屏障、控制台工作区、修订保留清理均未交付。
- 实机演示为功能性证据，不构成图规模容量或生产 HA 声明；`multiIf` 展开的执行成本待切片②用真实规模数据补测。
