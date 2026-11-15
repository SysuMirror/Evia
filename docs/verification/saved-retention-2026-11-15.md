# 留存分析保存模板端到端验证记录（2026-11-15）

## 背景与缺口

留存（`retention`）、分群、路径、归因四类高级分析此前只能即席运行：保存分析模板在
契约、`analysis.ValidateTemplate` 与 PostgreSQL `saved_analyses.kind` CHECK 三处都被
硬限制为 `trend`/`funnel`。本切片先打通**留存一类端到端**，立稳可复制的模式，其余三类
（分群/路径/归因）沿用同一模式后续交付。

## 链路与改动

完整链路：保存草稿 → 严格契约校验 → 模板结构校验 → 依赖固定（selection_mode + pin hash）
→ 不可变版本发布 → 运行时 `Bind`。

| 层 | 改动 |
| --- | --- |
| 契约 | `saved-analysis-template.schema.json` kind 枚举增 `retention`，新增 retention 属性引用与条件规则（必须含 retention+subject，禁止 event_versions/group_by/metrics/interval/funnel）；OpenAPI `QueryDependencyManifest.selection_mode` 闭集增 `retention_steps` |
| 模板 | `analysis.Template` 增 `Retention` 字段；`validatedRequest` 由二元判断改为显式闭集 switch，留存经起止 Step 钉版并拒绝顶层 `event_versions`，结构校验复用既有 `ValidateConfiguration`/`validateRetention` |
| 依赖固定 | 策略层 `savedDependencies` 与存储层 `reporting_saved.go` 双处新增 retention 分支：mode=`retention_steps`，从 `StartEvent`+`ReturnEvent` 收集去重后的钉版事件；`savedAnalysisDiff` 补 `/query/retention` 变更追踪 |
| 迁移 | `026_saved_analysis_retention.sql` 以 021 同款 DROP/ADD 范式把 `saved_analyses_kind_check` 放宽为 `('trend','funnel','retention')`，仅扩展、无 backfill |
| 契约守护 | `verify_contracts.py` 新增留存模板正例与 6 个负例（event_versions、缺 subject、混入 funnel、错误粒度、越界期数、未钉版起始事件） |

## 不变量

- trend/funnel 既有结果与 pin hash 逐字节不变；新枚举值与新字段均为增量。
- 留存模板仍拒绝执行期上下文（environment/time_range/timezone/event_keys/cursor/resolved_type），
  运行期由 `Bind` 注入。
- 依赖固定在策略层先做闭集匹配，真实定义哈希由存储事务查 `catalog_versions` 复核，
  漂移仍失败关闭，不削弱既有 pin 完整性。
- 分群/路径/归因在模板层仍以 `UNSUPPORTED_QUERY_KIND` 拒绝，本切片不放开。

## 先红（变异取证）

对 `saved_analysis_policy.go` 注入两处变异并还原（sha256 校验一致）：

- 变异 A：把留存的 `selection_mode` 强制回退为 `event_versions`
  → `TestSavedValidationRetentionPinsBoundarySteps` 报 `INVALID_ANALYSIS_DEPENDENCIES` 变红。
- 变异 B：移除起止 Step 的钉版收集，使 pin 集合为空
  → 同一测试因依赖数量不匹配报 `INVALID_ANALYSIS_DEPENDENCIES` 变红。

单元层另把原 `TestRetentionRejectedFromSavedTemplates`（断言留存被拒）改写为
`TestRetentionRejectedFromSavedTemplates`→`TestRetentionSavedTemplateValidatesPinsAndBinds`，
覆盖正向规范化/绑定与 7 个失败关闭（顶层 event_versions、缺 retention、缺 subject、
错误粒度、越界期数、未钉版事件、趋势专用 metrics）。

## 真实 PostgreSQL 端到端

迁移经 `//go:embed migrations/*.sql` 加载后，用宿主 55432 的 dev Postgres（隔离 schema）运行
`TestSavedAnalysisRetentionPersistsAndPinsBoundarySteps`：发布 sign_up/login 两个事件 →
创建留存保存分析（证明放宽后的 kind CHECK 放行）→ 校验得到 `retention_steps` 与 2 个边界
pin → 发布版本 1 且 `DefinitionHash` 与校验哈希一致。迁移若不放宽，该插入会被 PG 以 23514
CHECK 违约拒绝，因此测试对迁移具有真实判别力。

## 验证命令与结果

```
go test ./internal/analysis/ ./internal/reporting/ -count=1
# ok，含留存模板与 retention_steps 依赖 pin 用例
EVIA_TEST_DATABASE_URL=... go test -tags=integration ./internal/storage/postgres \
  -run TestSavedAnalysisRetentionPersistsAndPinsBoundarySteps -count=1
# ok（真实 PG，迁移 026 全链路）
python tooling/verify_contracts.py
# 54 个保存分析负例全部通过（新增 6 个留存负例）
make check PYTHON=<venv>/python
# 退出码 0：32 个 Go 包 -race 全 ok、零 FAIL，prettier/typecheck/api-client/browser-sdk/构建全过
```

## 边界

本切片只交付留存模板的后端、契约与 API/机器客户端端到端能力。人类控制台保存分析编辑器
当前仍只暴露 trend/funnel 入口（类型检查已确认契约扩展对现有控制台无破坏），留存的可视化
表单作为下一个独立前端切片；分群/路径/归因模板随后按本模式同构放开。
