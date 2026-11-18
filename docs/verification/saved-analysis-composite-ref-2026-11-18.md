# 保存分析复合公式指标引用（saved analysis composite metric ref）验证记录（2026-11-18）

## 问题（确定性缺口）

复合公式指标已全链路交付，但保存分析与看板的 `metric_ref` 只能解析原子表 `metric_versions`（`readMetricVersionTx`），复合 ID 无行即失败；feature-coverage 行 28 的真实剩余边界正是「复合指标的保存分析/看板复用未接通」。本切片先打通保存分析侧，看板三目标外键留下一切片。

## 设计决策（Option A：显式 kind 判别）

| 选项 | 取舍 | 结论 |
| --- | --- | --- |
| **A. `metric_ref` 增加显式 `kind: atomic\|composite`** | 与迁移 `033` 公式依赖双目标外键同构；无 UUID 碰撞歧义；契约即事实、fail-closed | 采用 |
| B. 存储层隐式双表探测 | 契约不变但两表 UUID 同空间存在歧义、隐式行为不可从契约读出，违背显式风格 | 拒绝 |

关键零漂移约束：`kind` 可选，**省略与显式 `atomic` 都不在存储/JSON 中物化默认值**（校验只判定不写回），历史原子引用的 JSON 字节与以 `MetricReference` 为键的公式映射完全不变；`IsComposite()` 仅在显式 `composite` 时为真。开发中一度把空值归一化写回 `"atomic"`，立即导致 5 个既有公式/卡片测试失败，已回退为只校验不写回。

## 链路改动

1. **契约**：共享 `MetricReference` 组件新增可选 `kind` 闭集（OpenAPI 手写权威源 + codegen 生成 Go bundle/TS 类型）。枚举值零变化，仅加一个可选字段。
2. **领域**（`reporting`）：`MetricReference.Kind`、常量、`IsComposite()`、`NormalizedMetricRefKind()`；新增 `ResolveCompositeMetricReference` 与 `normalizeResolvedCompositeDefinition`。复合定义无事件选择，无法派生原子 trend 模板，存储查询是 `{kind:"trend"}` 标记 shell（禁带 metrics/event_versions/interval/group/filter），执行由内部 `formula_ref` 标记驱动。
3. **存储**（`postgres`）：
   - 写入 `resolveSavedDefinitionTx` 按 kind 分支读 `formula_metric_versions`；
   - 依赖清单 pin 记录公式版本 `DefinitionHash`（复用 `MetricDependency`，公式原子叶子仍固定在不可变公式版本内）；
   - 运行 `verifySavedMetricPinTx` 复合分支复核哈希（漂移即 `METRIC_PIN_CONFLICT`）；
   - 入队 `EnqueueSavedAnalysisVersion` 复合分支镜像公式运行的 31 天/IANA 闭集，构造带 `analysis.FormulaJobRef` 的标记请求，由 query worker 的 `CompositeFormulaEngine` 递归展开 DAG；
   - 看板 `NormalizeDashboardDefinition` 对复合引用显式 409 式 fail-closed（`INVALID_METRIC_REFERENCE`），直到三目标见证落地。
4. **控制台**：`MetricReferenceSection` 增加指标类型切换，复合走 `listFormulaMetrics`/`listFormulaMetricVersions`，原子保持无 kind 字段；模型层透传 kind。
5. 无迁移：保存分析的指标 pin 是事务内应用级校验 + 清单哈希（无硬外键表），定义 CHECK 只要求 query 对象带 kind，标记 shell 满足。

## 验证

### 单元（`reporting`，纯函数）
新增 6 个：kind 归一化/`IsComposite`、复合输入归一化、标记 shell 构造与存储分派、不匹配/原子误入拒绝、shell 拒带原子体、看板复合拒绝。整包 `go test` 通过，既有公式引擎/对齐/计划与原子卡片测试零改动通过。

### 真实 PostgreSQL 集成（`-tags integration`，隔离 schema + 全量迁移）
新增 2 个：
- `TestSavedAnalysisCompositeReferenceResolvesAndEnqueuesMarker`：创建/发布复合指标→保存分析复合引用→断言标记 shell、pin=公式哈希、发布后入队作业的 `request.formula_ref` 携带正确 id/version；
- `TestSavedAnalysisCompositeReferenceRejectsMissingVersion`：缺失版本与非法 kind 均 fail-closed。
回归 `TestSavedAnalysis|TestFormulaMetric|TestDashboard` 整组通过（33s）。

### 变异红绿（基线 `/tmp/evia-check/saved-composite-ref/fe/SHA256SUMS`）
1. 入队 `Formula` 标记置 nil → 集成测试红（作业缺 formula_ref），还原 sha256 一致复绿；
2. 删除看板复合拒绝守卫 → 领域单测红，还原 sha256 一致复绿。

### 前端
`tsc --noEmit` 通过；Playwright model 规格（临时无 webServer 配置，用后即删）saved-analysis 14/14、metrics+formula-metrics 14/14 通过，含新增「复合显式 kind、原子无 kind」用例。

### 门禁
全量 `make check` EXIT=0、零 FAIL（Go 32 包、client 49、sdk 63、生产构建）。日志：`/tmp/evia-check/saved-composite-ref/make-check.log`。

## 明确未做（边界）

- 看板复合组件：需迁移把 `dashboard_dependencies`/`dashboard_run_widgets` 双目标扩为含 `formula_metric_versions` 的三目标见证（soundness CHECK、PL/pgSQL 形状函数、完整性触发器），为独立下一切片；当前显式 fail-closed。
- 不改任何枚举值、状态机或公式执行语义；不引入新迁移、新权限或新队列表。
- 漏斗严格有序、身份关联图、来源系统字段分析化、热力图/会话回放与外部基础设施仍待单独拍板或外部环境。
