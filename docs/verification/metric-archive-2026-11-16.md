# 原子指标软归档生命周期验证记录（2026-11-16）

## 问题（确定性缺口）

三大版本化资源族中，看板（迁移 029）与保存分析（迁移 030）已有软归档，报告有硬删除，唯独原子指标（metrics）没有任何生命周期终点：废弃/过时指标永久堆积在默认列表与各类指标选择器中，无法清理工作区。`docs/plans/custom-dashboards.md` 也把指标/分析「生命周期实体」列为长期方向。

## 范围与语义决策

本轮交付**原子指标**（不含复合公式指标，留下一切片）后端 + 契约 + 控制台全链路，复用已两次验证的归档模式，默认行为逐字节不变：

1. **软标记而非删除**：迁移 031 给 `metrics` 加可空 `archived_at`/`archived_by`、活跃部分索引 `metrics_active_scope` 与 soundness CHECK。既有 `metric_preserve_scope` 触发器只保护 project/environment/metric_id/created_at，标记与 updated_at 更新无需改触发器。
2. **不可变版本即事实，跨资源引用不受影响（关键决策）**：保存分析的 `metric_ref`（经 `verifySavedMetricPinTx` 读 `metric_versions`）与复合公式指标 DAG（`formula_metric_dependencies` 外键到 `metric_versions`）都只引用不可变版本表、从不回读可变父行。归档父指标不破坏既有保存分析/公式指标版本与运行，也不扫描引用做阻断；指标自身的新草稿/校验/发布/运行在父行 fail-closed。
3. **可逆双动作**：`archive`/`unarchive`，重复归档 `METRIC_ALREADY_ARCHIVED`、对活跃指标恢复 `METRIC_NOT_ARCHIVED`，显式冲突而非静默幂等。
4. **归档后自身变更全部 fail-closed**：草稿保存、校验快照（覆盖发布）、运行入队统一 409 `METRIC_ARCHIVED`；已建查询作业仍可查询/取消；按 ID/草稿/不可变版本仍可读。
5. **闭集 view + 游标视图绑定**：`GET P/metrics` 增加可选 `view=active|archived`，缺省 active；未知值 `INVALID_ARCHIVE_VIEW`；游标 scope 追加视图段，跨视图复用 `INVALID_CURSOR`。谓词仅由校验后布尔量在两个常量间选择，请求文本永不进 SQL。
6. **权限与审计**：两个动作复用 `metrics.write`（机器经精确授权同样适用，已加路由级机器权限测试），viewer 拒绝为 `PROJECT_FORBIDDEN`；审计写 `metric.archived`/`metric.unarchived`，与既有 `metric.*` 同表。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 迁移 | `031_metric_archive.sql` | 两可空列、部分索引、soundness CHECK |
| 领域 | `reporting/metric.go`、`metric_service.go` | `MetricRecord.ArchivedAt/ArchivedBy`；两接口加 Archive/Unarchive；List 加 `archivedOnly` |
| 存储 | `postgres/metrics.go`、`metrics_queries.go` | 列/扫描器（UTC 归一）、列表谓词、`assertMetricActiveTx`、两个生命周期方法、草稿/快照/入队守卫 |
| HTTP | `control/metrics.go` | 两条新路由（metrics.write）、列表 view 与游标绑定、两个处理器（复用共享 `savedArchiveQuery`） |
| 契约 | `openapi.json` | MetricRecord 加可空归档字段；list 加 view；两个新路径与完整 Problem 响应 |
| 生成 | wire bundle/types、`schema.gen.ts`、api-client | codegen；`MetricPage.view` 与 `archiveMetric`/`unarchiveMetric` |
| 控制台 | `metrics/metrics.tsx`、`styles.css` | 活跃/已归档双视图、归档确认 Modal、恢复、查询键/失效刷新 |

## 验证

### 路由单元测试（`internal/transport/control`）

新增 `TestMetricHTTPArchiveViewSelector`：缺省/active 转发 `archivedOnly=false`、archived 转发 `true`；bogus/重复 view 与条目路由携带 view 分别 400（`INVALID_ARCHIVE_VIEW`/`INVALID_QUERY`）且不触达服务；归档游标只在 `...:archived` scope 解码，回放到活跃列表 409 系外的 400 `INVALID_CURSOR`。路由契约表新增两条 200 写策略用例；机器权限表新增两条要求 `metrics.write`；全路由鉴权表纳入新路径；既有游标 scope 期望同步为 `...:active`。

### 真实 PostgreSQL 集成（隔离 schema，`-race -tags=integration`）

新增 `TestMetricArchiveLifecycleAndMutationGuards`（先发布再造归档）：viewer 归档禁止；归档成功带标记、重复归档 409；默认列表只留活跃指标、archived 流恰返回归档项且带标记；按 ID/草稿/不可变版本仍可读；草稿保存、校验、发布、运行入队四类变更全部 `METRIC_ARCHIVED`；未知 ID `METRIC_NOT_FOUND`；对活跃指标恢复 `METRIC_NOT_ARCHIVED`，恢复后标记清空、重新进入活跃列表、归档流为空；审计按序出现 `metric.archived`、`metric.unarchived`。

升级模拟 `TestMetricUpgrade...` 在仅含 pre-012 的旧基线 schema 上用当前代码播种；031 依赖 012 创建的 `metrics` 表，不能像 030（依赖早已存在的 saved_analyses）那样纳入基线，故仅 030 进基线、031 随 `Migrate()` 在 012 建表后应用，指标操作均发生在迁移之后，验证通过。

### 变异红绿（三轮，sha256 逐字节还原）

1. 反转列表 SQL 谓词 → 归档指标泄漏默认列表，集成测试变红（`archived metric must leave the default listing`）；
2. 移除运行入队守卫 → 归档指标成功入队（返回 nil 错误），集成测试变红（`run enqueue must reject archived metric: <nil>`）；
3. 移除共享助手 view 闭集拒绝 → 看板、保存分析、指标三个 `ArchiveViewSelector` 测试同时变红，证明共享助手对三类资源统一 fail-closed。

三轮均还原到基线（sha256 校验一致）后转绿。

### 客户端与门禁

- api-client 新增专用用例钉死 view 查询串编码顺序、archive/unarchive 的 POST/CSRF/无请求体/无幂等键与信号转发，`pnpm test:client` 38/38 通过；
- `pnpm typecheck` 零错误，`pnpm build` 控制台构建成功，prettier 通过；
- 完整 `make check` 退出码 0（32 个 Go 包 ok、0 FAIL）；codegen 幂等；gofmt/命名检查无禁词。

## 明确未做（边界）

- 复合公式指标（formula-metrics）的软归档未在本切片，是下一同构切片；其依赖的原子指标归档不影响已发布公式版本（外键见证不可变）；
- 归档不阻断对已发布不可变指标版本的新引用——刻意的不可变事实语义，选择器默认 active 视图自然隐藏归档项；若产品要求新组合不得引用已归档指标，需新增显式策略并评估 metric_ref 与公式 DAG 两条解析链；
- 控制台无组件级单测设施，前端以 tsc 严格类型、生产构建与既有请求槽模式保证；全栈浏览器 e2e 属凭证门控，不在默认 `make check`；
- 看板直接指标版本引用、单位换算、身份关联图仍为独立缺口。
