# 分群/路径/归因保存模板端到端验证记录（2026-11-15）

## 背景

上一切片打通了留存（retention）保存模板并立稳「类型化步骤钉版 + selection_mode 依赖固定」
模式。本切片把同一模式同构推广到剩余三类高级分析——分群（segment）、路径（path）、
归因（attribution），至此高级分析的保存模板后端能力全部就绪。人类控制台可视化表单仍为
独立前端切片，本切片交付 API/机器客户端端到端能力。

## 依赖固定建模

| Kind | selection_mode | 钉版事件来源 | pin 数 |
| --- | --- | --- | --- |
| segment | `segment_steps` | `segment.event` | 1 |
| path（有起始） | `path_steps` | `path.start_event` | 1 |
| path（无起始） | `all_events` | 无（读取窗口内全部事件） | 0 |
| attribution | `attribution_steps` | `conversion` + 1–8 个 `touchpoints` | 2–9 |

无起始事件的路径是关键特例：其 ClickHouse 计划拉取窗口内主体的前 depth 个事件，不钉任何
事件版本，因此复用既有 `all_events` 模式而非新设空 pin 枚举，语义与即席校验完全一致。

## 改动

- 迁移 `027_saved_analysis_advanced_kinds.sql`：以 026 同款 DROP/ADD 把
  `saved_analyses_kind_check` 扩展为六类，仅放宽、无 backfill。
- 契约：模板 schema kind 枚举增三类，各带条件规则（强制类型块+subject、拒绝外来旋钮）；
  OpenAPI `selection_mode` 闭集增 `segment_steps`/`path_steps`/`attribution_steps`
  （`all_events` 已存在）；再生成 wire/TS。
- 模板：`analysis.Template` 增 Segment/Path/Attribution 字段并纳入规范化重建；kind 门控
  改为显式闭集，三类经各自类型块钉版并拒绝顶层 `event_versions`，结构校验完全复用
  `ValidateConfiguration` 与 `WalkFields`（后者已覆盖三类嵌套块的 ResolvedType 剥离）。
- 依赖固定：策略层 `savedDependencies` 与存储层 `reporting_saved.go` 双处对称新增三分支；
  `savedAnalysisDiff` 补 `/query/segment`、`/query/path`、`/query/attribution` 变更追踪。

## 先红（变异取证）

对 `saved_analysis_policy.go` 注入并还原（sha256 校验一致）：

- 变异 A：归因模式强制回退 `event_versions`
  → `TestSavedValidationAttributionPinsConversionAndTouchpoints` 报 `INVALID_ANALYSIS_DEPENDENCIES`。
- 变异 B：无起始路径被错误标记为 `path_steps`（应为 `all_events`）
  → `TestSavedValidationPathPinsStartOrReadsAllEvents` 报 `INVALID_ANALYSIS_DEPENDENCIES`。

单元层把三个旧的 `Test*RejectedFromSavedTemplates`（断言被拒）改写为正向 `ValidateTemplate`+
`Bind` 与失败关闭子测（顶层 event_versions、缺类型块、缺 subject、各自越界配置、趋势专用
metrics），与留存测试同构。

## 真实 PostgreSQL 端到端

`TestSavedAnalysisAdvancedKindsPersistWithTypedPins` 在宿主 55432 dev PG（隔离 schema）发布
sign_up/open/buy/ad_click 后，分别创建四类保存分析并校验：放宽后的 kind CHECK 放行、
manifest 模式与 pin 数正确（segment=1、pinned path=1、unpinned path=all_events/0、
attribution=2）、发布版本 1 成功。过程中抓到一处测试侧问题：分群字段最初引用了夹具 schema
不存在的 `plan` 属性，被真实目录以 `QUERY_PROPERTY_NOT_DEFINED` 拒绝；改为已发布的
`lesson_id` 后通过，证明存储层属性归属校验对新模板真实生效。

## 验证命令与结果

```
go test ./internal/analysis/ ./internal/reporting/ -count=1
# ok，含四类模板与依赖 pin 用例
EVIA_TEST_DATABASE_URL=... go test -tags=integration ./internal/storage/postgres \
  -run 'TestSavedAnalysisAdvancedKinds|TestSavedAnalysisRetention' -count=1
# ok（真实 PG，迁移 026/027 全链路）
python tooling/verify_contracts.py
# 64 个保存分析负例全部通过（本切片新增 10 个）
make check PYTHON=<venv>/python
# 退出码 0：32 个 Go 包 -race 全 ok、零 FAIL，prettier/typecheck/客户端/SDK/构建全过
```

## 不变量与边界

- trend/funnel/retention 既有 pin hash 与响应逐字节不变；新枚举、新字段均为增量。
- 分群/路径/归因在模板层的属性归属、主体、预算闭集仍由既有 ValidateConfiguration fail-closed。
- 本切片不含人类控制台表单；控制台保存分析编辑器当前仍只暴露 trend/funnel 入口，契约扩展对
  现有控制台零破坏（typecheck 通过），四类的可视化编辑统一作为下一前端切片。
