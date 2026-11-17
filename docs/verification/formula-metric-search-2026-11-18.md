# 复合公式指标标题搜索验证记录（2026-11-18）

## 问题（确定性缺口）

看板、保存分析与原子指标列表已交付统一的 `q` 标题搜索契约，复合公式指标列表仍只支持环境与 `view=active|archived` 闭集过滤。本切片把同一契约复用到复合公式指标列表，至此四类版本化资源（看板、保存分析、原子指标、复合公式指标）的列表搜索全部对齐，不引入新表或迁移。

## 范围与语义决策

1. **零新搜索内核**：归一化与 LIKE 转义复用 reporting 包的 `NormalizeCatalogSearch`/`EscapeLikePattern`，本切片只改签名、谓词与边界。
2. **独立白名单**：新增仅复合公式指标使用的 `formulaMetricListQuery`，不复用共享 `savedArchiveQuery`，保持每个资源的查询参数闭集显式可审。
3. **字面量匹配与游标绑定**：服务归一化后转义 `\`/`%`/`_`，存储以 `ESCAPE '\'` 的 ILIKE 合取谓词匹配；handler 把归一化词追加进签名游标 scope（`formula_metrics:actor:project:env:view:term`），跨词游标报 `INVALID_CURSOR`。
4. **控制台同构防抖**：沿用 300ms 防抖、即时值/提交值分离、提交词变化重置游标、空白省略 `q`、128 rune 客户端钳制。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 领域 | `reporting/formula_metric.go`、`formula_metric_service.go`、`formula_metric_service_test.go` | 列表签名加 `search`、复用共享归一化+转义、假存储与边界用例同步 |
| 存储 | `postgres/formula_metrics.go`、`formula_metric_search_integration_test.go`（新）、`formula_metrics_integration_test.go` | `title ILIKE ... ESCAPE '\'` 合取谓词、`$5=''` 关闭过滤、既有调用点补参 |
| 契约/HTTP | `openapi.json`、生成 `schema.gen.ts`、`transport/control/reporting_saved.go`、`formula_metrics.go`、`formula_metrics_test.go` | 独立 `formulaMetricListQuery`、游标 scope 加搜索段、未知参数/超长词 400、替换上一切片的拒 q 断言 |
| api-client | `packages/api-client/src/index.ts`、`index.test.ts` | `FormulaMetricPage.search?`、复用 `catalogQuery` 的 q 编码、新增编码测试 |
| 控制台 | `formula-metrics/model.ts`、`formula-metrics.tsx`、`styles.css`、`tests/browser/formula-metrics-model.spec.ts` | 归一化纯函数、防抖、游标重置、工具栏搜索框、模型测试 |

## 验证

### 真实 PostgreSQL 集成（`-tags=integration`，容器 `evia-development-postgres-1:55432/evia_test`）

- `TestFormulaMetricSearchFiltersByTitleSubstring`：夹具基线 + 4 个目标，空词返回全量 5 条；中文子串 2 条、拉丁大小写不敏感 1 条；转义 `%`/`_` 只命中含该字面量的标题；无匹配空非 nil 页；staging 搜索不跨环境。
- `TestFormulaMetricSearchCombinesArchive`：两条同词复合指标归档其一后，活跃搜索减 1、归档搜索仍命中，证明归档谓词与搜索合取。

### 领域、HTTP 边界与客户端

- `TestFormulaMetricHTTPListSearchTerm`：trim 后到达假服务、limit=1 游标只能在含词 scope 解码、未知参数 400、129 字词语音 400 且不到达服务；硬编码 scope 字符串同步为尾冒号形式。
- api-client 全套 48 项通过（新增 1 项 q 编码）；codegen 后 wire 类型零变化、`schema.gen.ts` 仅增 `q?` 两行。
- Playwright 模型套件 101 项通过，新增复合指标搜索用例覆盖 trim、空白、128/129 rune、emoji 码点计数与内部空白保留。

### 变异红绿（sha256 逐字节还原，禁用 git checkout）

后端两轮（基线 `/tmp/evia-check/formula-search/SHA256SUMS`，3 文件）：

1. 存储谓词置 `($5='' OR TRUE)` → 子串过滤集成测试变红（归档合取用例因夹具仅 2 条同词记录而巧合不受影响，过滤用例精确捕获变异）。
2. `formulaMetricListQuery` 移除 `q` → HTTP 搜索请求 400，边界测试变红。

前端两轮（基线 `/tmp/evia-check/formula-search-fe/SHA256SUMS`，2 文件）：

3. 客户端钳制截断改为 `maxFormulaMetricSearchRunes + 1` → 新模型用例精确变红。
4. 移除 `normalizeFormulaMetricSearch` 导入但保留调用 → tsc 报 TS2304。

每轮还原后 `sha256sum -c` 全部 OK 并复跑复绿。

### 门禁

- `make check PYTHON=…/venv/bin/python` 一次通过（命名、契约、边界、监控、后端 `-race` 全量包、prettier、typecheck、test:client 48 项、SDK、生产构建全绿）。
- 全量 PG 集成包 `go test -race -tags=integration -count=1 -skip TestFailoverBaselineWrite ./internal/storage/postgres` 一次通过（243.7s，0 FAIL，无 flake）。

## 明确未做（边界）

- 仅标题子串匹配，不做描述/字段全文检索，不引入索引或搜索引擎。
- 控制台无组件级单测设施，搜索交互以纯函数模型测试 + tsc 变异红绿 + 生产构建为证据。
- 文件夹侧栏内复制入口等看板规划剩余项与本切片无关，保持原状。
