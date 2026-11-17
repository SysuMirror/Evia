# 看板指标组件控制台接线验证记录（2026-11-17）

## 问题（确定性缺口）

迁移 034 已在后端、契约与真实 PostgreSQL 交付看板 widget 二选一固定来源（保存分析版本或不可变指标版本，见 [后端验证记录](dashboard-metric-widget-2026-11-17.md)），但控制台仍按 analysis-only 模型工作，存在四个确定性缺口：

1. 组件选择器只能列出保存分析，无法选择已发布指标，`onAdd` 载荷只有 `SavedAnalysisVersion` 一种；
2. 运行视图对每个 widget 无条件读取 `analysis_id`/`analysis_version` 并预取保存分析版本，metric widget 在该路径下取不到 `analysis`，只能落入「尚未运行」空态，结果永远不可达；
3. 运行时筛选的字段目录只从保存分析的固定事件推导，metric widget 即使出现在目标组件下拉中也只剩系统字段，与其后端 `MetricExecute.Filter` 能力不匹配；
4. 发布前校验的依赖表把所有见证行按 `analysis_version` 渲染，metric 行会显示 `vundefined`，且编辑器卡片摘要写死「分析 vN」。

## 范围与语义决策

本切片为**纯控制台垂直切片**，无后端、契约与数据库变更，全部语义以后端三层（领域规范化、OpenAPI、DB CHECK）既有强制为准：

1. **联合来源载荷而非可选字段堆叠**：新增 `DashboardWidgetSource` 判别联合（`{kind:'analysis',analysis}` | `{kind:'metric',metric,version}`），选择器按来源切换列表/版本查询，提交端构造恰一来源的 widget；切换来源整体替换引用但保留 `widget_id`、布局与阅读顺序。
2. **客户端二选一校验是辅助防线**：`dashboardIssues` 镜像服务端不变量（恰一来源，否则禁止保存/发布），真正的强制仍在服务端与 CHECK 约束；前端校验不通过时发布按钮不可用。
3. **metric 运行态走通用 QueryResult 渲染**：metric widget 没有固定分析定义可推导种类专属列名，列直接取 `QueryResult.columns` 的 `label/key/type`，单元格复用保存分析既有的类型感知 `displayCell`（datetime 时区、null/缺失、对象 JSON、数值本地化），精度/覆盖徽标、有界结果表、元数据与原始 JSON 明细与分析卡片保持同等信息密度。
4. **运行时筛选与后端能力对齐**：运行视图额外预取 metric widget 固定的 `MetricVersion`，筛选模态的字段目录合并分析 pins 与指标 `definition.event_versions`，并按 widget 来源分别限定可选字段；目标组件下拉标注（指标/分析）。
5. **门控不放宽**：分析版本与指标版本预取全部完成前，运行按钮、筛选按钮与执行 hook 均禁用；401/403 统一走 `onDenied`。

## 改动清单

| 文件 | 说明 |
| --- | --- |
| `dashboards/types.ts` | 新增 `DashboardWidgetSource` 判别联合 |
| `dashboards/picker.tsx` | 来源切换、`listMetrics`/`listMetricVersions` 双目录、分页与 ready 分支、联合 `onAdd` |
| `dashboards/editor.tsx` | `add()` 双源构造与换源保留身份/布局、卡片摘要改 `widgetSourceLabel`、中性「更换引用版本」文案 |
| `dashboards/model.ts` | 新增 `widgetSourceLabel`；`dashboardIssues` 客户端恰一来源校验 |
| `dashboards/run.tsx` | metric 版本预取与统一 `sourcesReady` 门控；渲染循环双源分支；新增 `MetricWidgetPresentation`（通用结果表/徽标/元数据/状态恢复） |
| `dashboards/runtime-filters.tsx` | 新增 `metrics` 入参；pins 合并指标 `event_versions`；`fields()` 按 widget 来源限定；目标组件下来源标注 |
| `dashboards/publication-review.tsx` | 依赖见证表按 metric/analysis 双源渲染固定版本，文案改为中性「固定组件依赖/版本」 |

## 验证

### 静态门禁（前端）

- `pnpm typecheck`（全工作区 `tsc --noEmit`）退出码 0；
- `pnpm build` 控制台生产构建成功，`dashboards` chunk 57.39 kB（gzip 17.43 kB）；
- `pnpm exec prettier --write` 后 `make check` 内 `prettier --check` 全量通过。

### 变异红绿（两轮，sha256 逐字节还原）

基线哈希记录于 `/tmp/evia-check/dashboard-metric-console-baseline.sha256`（7 个改动文件）：

1. 移除 `run.tsx` metric 预取的类型谓词（退化为全量 widget 过滤）→ `tsc` 报 6 处 TS18048「`w.metric_ref` is possibly 'undefined'」，证明窄化谓词是 metric 路径的承重检查；
2. 在 `editor.tsx` metric 分支注入 `analysis_id: null`（模拟双来源载荷）→ `tsc` 在新增/替换两个 `DashboardWidget` 构造点报 TS2322，证明联合载荷在构造期即受契约类型约束。

两轮均用 Edit 精确还原，`sha256sum -c` 七个文件全部「成功」，复跑 `pnpm typecheck` 退出码 0。

### 全量门禁与后端回归

- 完整 `make check`（`PYTHON` 指向宿主 venv）退出码 0：32 个 Go 包 ok、0 FAIL；api-client 客户端测试 39/39；其余 Node 测试 18/18、57/57；gofmt、命名检查（无禁词）、OpenAPI 契约校验、prettier、tsc 全绿；
- 真实 PostgreSQL 全量集成包 `-tags=integration -count=1 -skip 'TestFailoverBaselineWrite'` 新鲜运行通过（84.2s，0 FAIL）；其中看板套件 verbose 复跑 12 个用例全 PASS，含本能力的后端见证 `TestDashboardMetricWidgetLifecycle`（混合看板双源见证、metric 扇出与错配拒绝）。`TestFailoverBaselineWrite` 属既有多主机环境门控（需独立 `EVIA_TEST_PG_FAILOVER_DSN`），与本切片无关。

## 明确未做（边界）

- 控制台无组件级单测设施，前端以 tsc 严格类型、生产构建、prettier 与变异红绿保证；全栈浏览器 e2e 属凭证门控，不在默认 `make check`，本轮未新增 Playwright 用例；
- 逻辑层防线（`dashboardIssues` 恰一来源、筛选字段按来源限定）的最终强制权在服务端：widget 来源由领域规范化 + DB CHECK 拒绝，越权字段由后端查询校验拒绝，前端只做提前失败与可用性约束；
- metric widget 仍以指标固定口径生成 trend 通用结果表，不提供分析卡片的种类专属图表（漏斗/留存等），看板级覆盖聚合或参数绑定不在本切片；
- 看板文件夹、通用参数映射、比较期、跨请求结果缓存仍为独立缺口。
