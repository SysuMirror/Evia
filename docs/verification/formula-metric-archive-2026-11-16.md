# 复合公式指标软归档生命周期验证记录（2026-11-16）

## 问题（确定性缺口）

软归档已覆盖看板（迁移 029）、保存分析（迁移 030）与原子指标（迁移 031），三大版本化资源族中唯独复合公式指标（formula-metrics）没有生命周期终点：废弃/过时的复合指标永久堆积在默认列表中，无法清理工作区，也无法阻止在过时父指标上继续改草稿、发版本或入队运行。本切片补齐最后一块同构能力，使四类资源的归档语义完全对齐。

## 范围与语义决策

本轮交付**复合公式指标**后端 + 契约 + 控制台全链路，复用已三次验证的归档模式，默认行为逐字节不变：

1. **软标记而非删除**：迁移 032 给 `formula_metrics` 加可空 `archived_at`/`archived_by`、活跃部分索引 `formula_metrics_active_scope` 与 soundness CHECK（两标记必须同时为空或同时非空）。既有 `formula_metric_preserve_scope` 触发器只保护 project/environment/metric_id/created_at，标记与 updated_at 更新无需改触发器（已在迁移注释说明）。
2. **不可变版本即事实，跨资源引用不受影响（关键决策）**：复合指标对原子指标的依赖通过 `formula_metric_dependencies` 外键哈希见证引用 `metric_versions` 不可变表、从不回读可变父行。归档复合父指标不破坏其已发布版本、依赖见证与历史运行；被它引用的原子指标若被归档，已发布复合版本同样不受影响。复合指标自身的新草稿/校验/发布/运行在父行 fail-closed。
3. **可逆双动作**：`archive`/`unarchive`，重复归档 `FORMULA_METRIC_ALREADY_ARCHIVED`、对活跃指标恢复 `FORMULA_METRIC_NOT_ARCHIVED`，显式冲突而非静默幂等；未知 ID 一律 `FORMULA_METRIC_NOT_FOUND`。
4. **归档后自身变更全部 fail-closed**：草稿保存、校验快照（覆盖发布路径）、运行入队统一 409 `FORMULA_METRIC_ARCHIVED`；已建查询作业仍可查询/取消；按 ID/草稿/不可变版本仍可读。
5. **闭集 view + 游标视图绑定**：`GET P/formula-metrics` 增加可选 `view=active|archived`，缺省 active；未知值 `INVALID_ARCHIVE_VIEW`；游标 scope 由 `formula_metrics:actor:project:env` 追加视图段，跨视图复用 `INVALID_CURSOR`。谓词仅由校验后布尔量在两个常量间选择，请求文本永不进 SQL。
6. **权限与审计**：两个动作复用 `metrics.write`（机器经同一 `projectPermissionAuth` 中间件精确授权，通用中间件测试已覆盖 exact-grant/deny/CSRF），viewer 拒绝为 `PROJECT_FORBIDDEN`；审计写 `formula_metric.archived`/`formula_metric.unarchived`，与既有 `formula_metric.*` 同表。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 迁移 | `032_formula_metric_archive.sql` | 两可空列、活跃部分索引、soundness CHECK |
| 领域 | `reporting/formula_metric.go`、`formula_metric_service.go` | `FormulaMetricRecord.ArchivedAt/ArchivedBy`；Store/Service 两接口加 Archive/Unarchive；List 加 `archivedOnly`（服务层透传 limit+1） |
| 存储 | `postgres/formula_metrics.go`、`formula_metric_queries.go` | 列/扫描器（UTC 归一）、列表常量谓词、`assertFormulaMetricActiveTx`、两个生命周期方法、草稿/快照守卫；入队路径调整为项目锁→归档守卫→读版本顺序 |
| HTTP | `control/formula_metrics.go` | 两条新路由（metrics.write）、列表改用共享 `savedArchiveQuery` 并把游标 scope 绑定视图、两个处理器（无请求体，200 返回 FormulaMetricRecord） |
| 契约 | `openapi.json` | FormulaMetricRecord 加可空归档字段；listFormulaMetrics 加 view；两个新路径与完整 Problem 响应 |
| 生成 | wire bundle/types、`schema.gen.ts`、api-client | codegen；`FormulaMetricPage.view` 与 `archiveFormulaMetric`/`unarchiveFormulaMetric` |
| 控制台 | `formula-metrics/formula-metrics.tsx`、`styles.css` | 活跃/已归档双视图、归档确认 Modal、恢复、查询键失效刷新 |

## 验证

### 路由单元测试（`internal/transport/control`）

新增 `TestFormulaMetricHTTPArchiveViewSelector`：缺省/active 转发 `archivedOnly=false`、archived 转发 `true`；bogus/重复 view 与条目路由携带 view 分别 400（`INVALID_ARCHIVE_VIEW`/`INVALID_QUERY`）且不触达服务；归档游标只在 `formula_metrics:...:archived` scope 解码，回放到活跃列表返回 400 `INVALID_CURSOR`，且无法在 active scope 解码归档游标。路由契约表新增两条 200 写策略用例（`access.writes` 断言为写）；严格输入表新增三条 view 用例。`formulaHTTPService` 桩补齐 List 布尔参量与 Archive/Unarchive 方法。

### 真实 PostgreSQL 集成（隔离 schema，`-tags=integration`）

新增 `TestFormulaMetricArchiveLifecycleAndMutationGuards`（先发布再造归档）：viewer 归档禁止（`PROJECT_FORBIDDEN`）；归档成功带标记、重复归档 409；默认列表只留活跃指标、archived 流恰返回归档项且带标记；按 ID/草稿/不可变版本仍可读；草稿保存、校验快照、发布、运行入队四类变更全部 `FORMULA_METRIC_ARCHIVED`；未知 ID `FORMULA_METRIC_NOT_FOUND`；对活跃指标恢复 `FORMULA_METRIC_NOT_ARCHIVED`，恢复后标记清空、重新进入活跃列表、归档流为空；审计按序出现 `formula_metric.archived`、`formula_metric.unarchived`。本切片中 `go test -tags=integration -run TestFormulaMetric` 共 12 个用例全部通过。

升级模拟 `TestMetricUpgradePreservesExistingImmutableFactsJobsAndGrants` 通过：其基线过滤条件 `entry.Name() >= "012_metrics.sql"` 自动排除 032（032 依赖 013 才创建的 `formula_metrics` 表，不能进 pre-012 基线），032 随 `Migrate()` 在 013 建表后应用，与 031 处理一致，无需改动测试。

### 变异红绿（三轮，sha256 逐字节还原）

基线哈希记录于 `/tmp/evia-check/formula-archive-baseline.sha256`：

1. 反转 `ListFormulaMetrics` SQL 谓词（active/archived 常量互换）→ 归档指标泄漏默认列表，集成测试变红（`archived formula metric must leave the default listing`）；
2. 移除 `EnqueueFormulaMetricVersion` 的 `assertFormulaMetricActiveTx` 守卫 → 归档指标成功入队（返回 nil 错误），集成测试变红（`run enqueue must reject archived metric: <nil>`）；
3. 移除共享助手 `savedArchiveQuery` 的 view 闭集拒绝 → `TestFormulaMetricHTTPArchiveViewSelector` 变红（bogus view 穿透到服务层返回 200 而非 400），证明共享助手对 formula 资源统一 fail-closed。

三轮均用 Edit 精确还原并 `sha256sum -c` 校验三个文件全部 OK，随后 HTTP 与 PG 测试重跑转绿。

### 客户端与门禁

- api-client 新增专用用例钉死 view 查询串编码顺序（environment→limit→view）、archive/unarchive 的 POST/CSRF/无请求体/无幂等键与信号转发，`pnpm test:client` 39/39 通过；
- `pnpm typecheck` 零错误，`pnpm build` 控制台构建成功，`pnpm exec prettier --check apps packages` 全部通过；
- 完整 `make check PYTHON=...venv/bin/python` 退出码 0（32 个 Go 包 ok、0 FAIL；含 naming/contracts/boundaries/monitoring/web 全部子检查）；codegen 幂等；gofmt/命名检查无禁词。

## 明确未做（边界）

- 归档不阻断对已发布不可变复合版本的新运行之外的引用——已发布版本仍可按 ID 读取与运行历史追溯；刻意的不可变事实语义，选择器默认 active 视图自然隐藏归档项；
- 公式资源文件历史上没有资源级机器权限矩阵与全路由鉴权测试（仅两个 HTTP 测试），机器授权语义由通用 `authentication_permissions_test.go` 的 `projectPermissionAuth` 中间件测试统一覆盖，本切片不新建覆盖全部 12 条旧路由的测试，避免越界扩张；
- 控制台无组件级单测设施，前端以 tsc 严格类型、生产构建与既有请求槽模式保证；全栈浏览器 e2e 属凭证门控，不在默认 `make check`；
- 看板直接指标版本引用、单位换算、嵌套复合指标、身份关联图仍为独立缺口。
