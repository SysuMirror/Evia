# 原子指标标题搜索验证记录（2026-11-18）

## 问题（确定性缺口）

看板与保存分析列表已交付标题搜索，原子指标列表仍只有环境与 `view=active|archived` 闭集过滤。本切片把同一套 `q` 字面量子串契约复用到原子指标列表，复用前一切片已提升的目录级共享搜索内核，完成领域、存储、HTTP、契约、客户端与控制台闭环，不引入新表或迁移。

## 范围与语义决策

1. **零新领域代码**：归一化与 LIKE 转义直接复用 reporting 包的 `NormalizeCatalogSearch`/`EscapeLikePattern`；本切片只改签名、谓词与边界，不复制第二份搜索逻辑。
2. **白名单继续按资源隔离**：新增仅原子指标使用的 `metricListQuery`；`savedArchiveQuery` 仍被复合公式指标列表共用且保持不含 q。复合公式指标收到 `q` 返回 `INVALID_QUERY`，由其归档视图测试的坏路径用例固定。
3. **字面量匹配与游标绑定**：服务归一化后转义 `\`/`%`/`_`，存储以 `ESCAPE '\'` 的 ILIKE 合取谓词匹配；handler 把归一化词追加进签名游标 scope（`metrics:actor:project:env:view:term`），跨词游标报 `INVALID_CURSOR`。
4. **控制台同构防抖**：指标列表状态原内联在 `metrics.tsx`，本次就地加入即时值/防抖提交值分离、300ms 防抖、提交词变化重置游标、空白省略 `q` 与 128 rune 客户端钳制，不重构组件结构。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 领域 | `reporting/metric.go`、`metric_service.go`、`metric_test.go` | 列表签名加 `search`、复用共享归一化+转义下发、假存储同步 |
| 存储 | `postgres/metrics.go`、`metric_search_integration_test.go`（新）、3 个既有集成测试、`cmd/evia` runtime fake | `title ILIKE ... ESCAPE '\'` 合取谓词、`$5=''` 关闭过滤、runtime scope 断言含 search |
| 契约/HTTP | `openapi.json`、生成 `schema.gen.ts`、`transport/control/reporting_saved.go`、`metrics.go`、`metrics_test.go`、`formula_metrics_test.go` | 独立 `metricListQuery`、游标 scope 加搜索段、未知参数/超长词 400、公式指标拒 q 断言 |
| api-client | `packages/api-client/src/index.ts`、`index.test.ts` | `MetricPage.search?`、复用 `catalogQuery` 的 q 编码、新增编码测试 |
| 控制台 | `metrics/model.ts`、`metrics.tsx`、`styles.css`、`tests/browser/metrics-model.spec.ts` | 归一化纯函数、防抖、游标重置、工具栏搜索框、模型测试 |

## 验证

### 真实 PostgreSQL 集成（`-tags=integration`，容器 `evia-development-postgres-1:55432/evia_test`）

- `TestMetricSearchFiltersByTitleSubstring`：中文子串 2 条、拉丁大小写不敏感 1 条；转义 `%`/`_` 只命中含该字面量的标题；无匹配空非 nil 页；staging 搜索不泄漏 dev 数据与基线行；空词返回全量 5 条。
- `TestMetricSearchCombinesArchive`：两条同词指标归档其一后，活跃搜索减 1、归档搜索仍命中，证明归档谓词与搜索合取。

### 领域、HTTP 边界与客户端

- `TestMetricHTTPListSearchTerm`：trim 后到达假服务、limit=1 游标只能在含词 scope 解码、未知参数 400、129 字词语音 400 且不到达服务；硬编码 scope 字符串同步为尾冒号形式。
- `TestFormulaMetricHTTPArchiveViewSelector` 新增公式指标列表拒 q 的 400 断言，固定白名单边界。
- api-client 全套 47 项通过（新增 1 项 q 编码）；codegen 后 wire 类型零变化、`schema.gen.ts` 仅增 `q?` 两行。
- Playwright 模型套件 100 项通过，新增指标搜索用例覆盖 trim、空白、128/129 rune、emoji 码点计数与内部空白保留。

### 变异红绿（sha256 逐字节还原，禁用 git checkout）

后端两轮（基线 `/tmp/evia-check/metric-search/SHA256SUMS`，3 文件）：

1. 存储谓词置 `($5='' OR TRUE)`（参数计数不变）→ 两个搜索集成测试同时变红。
2. `metricListQuery` 移除 `q` → HTTP 搜索请求 400，边界测试变红。

前端两轮（基线 `/tmp/evia-check/metric-search-fe/SHA256SUMS`，2 文件）：

3. 客户端钳制截断改为 `maxMetricSearchRunes + 1` → 新模型用例精确变红。
4. 移除 `normalizeMetricSearch` 导入但保留调用 → tsc 报 TS2304，证明防抖链确实消费共享归一化器。

每轮还原后 `sha256sum -c` 全部 OK 并复跑复绿。

### 门禁

- `make check PYTHON=…/venv/bin/python` 退出码 0：prettier 对新客户端测试长行重排后复跑通过；命名、契约、边界、监控、后端 `-race` 全量包与 Web（typecheck、test:client 47 项、SDK、生产构建）全绿。
- 全量 PG 集成包 `go test -race -tags=integration -count=1 -skip TestFailoverBaselineWrite ./internal/storage/postgres` 一次通过（241.8s，0 FAIL，无 flake）。

## 明确未做（边界）

- 复合公式指标列表在本切片交付时仍不接受 `q`；该边界已由下一切片（`formula-metric-search-2026-11-18.md`）同构打通，本记录保留交付当时的边界声明。
- 仅标题子串匹配，不做描述/字段全文检索，不引入索引或搜索引擎。
- 控制台无组件级单测设施，搜索交互以纯函数模型测试 + tsc 变异红绿 + 生产构建为证据。
