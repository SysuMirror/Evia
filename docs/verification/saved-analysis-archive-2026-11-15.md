# 保存分析软归档生命周期验证记录（2026-11-15）

## 问题（确定性缺口）

三类受管资源中，报告（reports）有硬删除、看板（dashboards）已有软归档（迁移 029），而保存分析（saved analyses）没有任何生命周期终点：废弃分析会永久堆积在默认列表，无法清理工作区，也没有归档视图。

## 范围与语义决策

本轮交付**后端 + 契约 + 控制台全链路**，完全复查看板归档已验证模式，默认行为逐字节不变：

1. **软标记而非删除**：迁移 030 给 `saved_analyses` 加可空 `archived_at`/`archived_by`、活跃部分索引 `saved_analyses_active_scope` 与标记 soundness CHECK。既有 `saved_analysis_preserve_scope` 触发器只保护 project/environment/id/created_at，标记与 updated_at 更新无需改触发器。
2. **列表隐藏、按 ID 与不可变版本可读**：默认列表用 `archived_at IS NULL` 过滤（部分索引支撑）；Get/草稿/版本读取路径不拦截。
3. **不可变版本即事实，消费方不受影响（关键决策）**：看板版本与报告通过外键 + 哈希见证引用 `saved_analysis_versions` 不可变表，从不回读可变父行。因此归档父行不破坏既有看板/报告版本、不阻断其运行，也不扫描引用关系做阻断——这与「已发布不可变版本独立于当前目录状态」的既有模型一致；新的草稿编辑/校验/发布/运行则在父行 fail-closed。
4. **可逆双动作**：`archive`/`unarchive` 两个 POST，重复归档 `SAVED_ANALYSIS_ALREADY_ARCHIVED`、对活跃分析恢复 `SAVED_ANALYSIS_NOT_ARCHIVED`，显式冲突而非静默幂等。
5. **归档后自身变更全部 fail-closed**：草稿保存、校验快照（同时覆盖发布路径）、运行入队统一 409 `SAVED_ANALYSIS_ARCHIVED`；既有查询作业仍可查询/取消。
6. **闭集 view 参数 + 游标视图绑定**：`GET P/saved-analyses` 增加可选 `view=active|archived`，缺省等同 active；未知值 `INVALID_ARCHIVE_VIEW`。游标 scope 追加视图段，跨视图复用一律 `INVALID_CURSOR`。谓词仅由校验后的布尔量在两个常量间选择，请求文本永不进 SQL。
7. **权限与审计不变**：两个动作复用 `analyses.write`，viewer 拒绝为 `PROJECT_FORBIDDEN`；审计写 `analysis.archived`/`analysis.unarchived`，与既有 `analysis.saved_*` 同表。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 迁移 | `030_saved_analysis_archive.sql` | 两可空列、部分索引、soundness CHECK |
| 领域 | `reporting/saved_analysis.go`、`saved_analysis_service.go` | `SavedAnalysis.ArchivedAt/ArchivedBy`；两接口加 Archive/Unarchive；List 加 `archivedOnly` |
| 存储 | `postgres/reporting_saved.go`、`reporting_queries.go` | 列/扫描器（UTC 归一）、列表谓词、`assertSavedAnalysisActiveTx`、两个生命周期方法、草稿/快照/入队守卫 |
| HTTP | `control/reporting_saved.go`（共享 `savedArchiveQuery` 助手复用上一切片） | 两条新路由、列表 view 与游标视图绑定、两个处理器 |
| 契约 | `openapi.json` | SavedAnalysis 加可空归档字段；list 加 view；两个新路径与完整 Problem 响应 |
| 生成 | wire bundle/types、`schema.gen.ts`、api-client | codegen 同步；`SavedAnalysisPage.view` 与 `archiveSavedAnalysis`/`unarchiveSavedAnalysis` |
| 控制台 | `saved-analyses/`（list、use-history、use-controller、styles）、`saved-analyses.tsx` | 双视图、归档确认 Modal、恢复、查询键/失效刷新 |

## 验证

### 路由单元测试（`internal/transport/control`）

新增 `TestSavedAnalysisHTTPArchiveViewSelector`：缺省/active 转发 `archivedOnly=false`、archived 转发 `true`；bogus/重复 view 与条目路由携带 view 分别 400（`INVALID_ARCHIVE_VIEW`/`INVALID_QUERY`）且不触达服务；归档游标只在 `...:archived` scope 解码，回放到活跃列表 400 `INVALID_CURSOR`。路由契约表新增两条 200 写策略用例；既有游标测试 scope 期望同步为 `...:active`。

### 真实 PostgreSQL 集成（隔离 schema，`-race -tags=integration`）

新增 `TestSavedAnalysisArchiveLifecycleAndMutationGuards`（先发布再造归档）：viewer 归档禁止；归档成功带标记、重复归档 409；默认列表只留活跃分析、archived 流恰返回归档项且带标记；按 ID/草稿/不可变版本仍可读；草稿保存、校验、发布、运行入队四类变更全部 `SAVED_ANALYSIS_ARCHIVED`；未知 ID `SAVED_ANALYSIS_NOT_FOUND`；对活跃分析恢复 `SAVED_ANALYSIS_NOT_ARCHIVED`，恢复后标记清空、重新进入活跃列表、归档流为空；审计按序出现 `analysis.archived`、`analysis.unarchived`。全部既有保存分析/查询机器集成测试仅追加布尔实参，语义不变。

### 变异红绿（三轮，sha256 逐字节还原）

1. 反转列表 SQL 谓词 → 归档分析泄漏默认列表，集成测试变红（`archived analysis must leave the default listing`）；
2. 移除运行入队守卫 → 归档分析成功入队（返回 nil 错误），集成测试变红（`run enqueue must reject archived analysis: <nil>`），证明守卫必要且有效；
3. 移除共享助手的 view 闭集拒绝 → 看板与保存分析两个 `ArchiveViewSelector` 测试同时变红，证明共享助手对两类资源双重 fail-closed。

三轮均还原到基线（sha256 校验一致）后转绿。

### 客户端与门禁

- api-client 新增专用用例钉死 view 查询串编码顺序、archive/unarchive 的 POST/CSRF/无请求体/无幂等键与信号转发，`pnpm test:client` 37/37 通过；
- `pnpm typecheck` 零错误，`pnpm build` 控制台构建成功，prettier 通过；
- 完整 `make check` 退出码 0（32 个 Go 包 ok、0 FAIL）；codegen 幂等；命名检查无禁词。

## 明确未做（边界）

- 归档不阻断看板/报告对已发布不可变版本的新引用——这是刻意的不可变事实语义，而非遗漏；若产品要求「新组合不得引用已归档分析」，需新增显式策略并评估对报告/看板两条解析链的影响；
- 控制台无组件级单测设施，前端以 tsc 严格类型、生产构建与既有控制器模式保证；全栈浏览器 e2e 属凭证门控，不在默认 `make check`；
- 看板文件夹、通用参数、比较期、看板/分析直接指标版本引用、身份关联图仍为独立缺口。
