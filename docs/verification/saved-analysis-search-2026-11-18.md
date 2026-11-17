# 保存分析标题搜索验证记录（2026-11-18）

## 问题（确定性缺口）

看板列表已交付标题搜索，但保存分析列表只有环境与 `view=active|archived` 两类闭集过滤，分析数量增长后无法按标题定位。本切片把同一套 `q` 字面量子串契约复用到保存分析列表，完成领域、存储、HTTP、契约、客户端与控制台闭环，不引入新表或迁移。

## 范围与语义决策

1. **共享目录级搜索内核**：原看板专用 `NormalizeDashboardSearch` 提升为 reporting 包的 `NormalizeCatalogSearch`（`catalog_search.go`），`EscapeLikePattern` 保持通用名；行为逐字节不变，仅改名与迁移文件，看板调用点与既有测试同步换名。三类资源标题均为 128 rune 上限，一个共享常量即可表达。
2. **白名单按资源隔离**：`savedArchiveQuery` 被保存分析、原子指标、复合公式指标三个列表共用，因此不在其中加 `q`；新增仅保存分析使用的 `savedAnalysisListQuery`。指标列表收到 `q` 仍返回 `INVALID_QUERY`，由 metrics 夹具的坏路径用例固定，避免搜索参数被静默忽略。
3. **字面量匹配与游标绑定**：服务归一化后转义 `\`/`%`/`_`，存储以 `ESCAPE '\'` 的 ILIKE 合取谓词匹配；handler 把归一化词追加进签名游标 scope（`saved_analyses:actor:project:env:view:term`），跨词游标报 `INVALID_CURSOR`。
4. **控制台同构防抖**：300ms 防抖、即时值与提交值分离、提交词变化重置分页、空白省略 `q`、客户端镜像 128 rune 钳制，与看板搜索框行为一致。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 共享领域 | `reporting/catalog_search.go`（新，替换 `dashboard_search.go`）、`catalog_search_test.go`、`dashboard_service.go`、`transport/control/dashboard.go` | 纯函数提升为目录级命名，看板行为不变 |
| 保存分析领域 | `reporting/saved_analysis.go`、`saved_analysis_service.go`、`saved_analysis_test.go` | 列表签名加 `search`、归一化+转义下发、假存储同步 |
| 存储 | `postgres/reporting_saved.go`、`saved_analysis_search_integration_test.go`（新）及既有调用点、`cmd/evia` runtime fake | `title ILIKE ... ESCAPE '\'` 合取谓词、`$5=''` 关闭过滤 |
| 契约/HTTP | `openapi.json`、生成 `schema.gen.ts`、`transport/control/reporting_saved.go`、两个 `*_test.go` | 独立 q 白名单、游标 scope 加搜索段、未知参数/超长词 400、指标列表拒 q 断言 |
| api-client | `packages/api-client/src/index.ts`、`index.test.ts` | `SavedAnalysisPage.search?`、复用 `catalogQuery` 的 q 编码、新增编码测试 |
| 控制台 | `saved-analyses/model.ts`（新）、`use-history.ts`、`list.tsx`、`styles.css`、`saved-analyses.tsx`、`tests/browser/saved-analysis-model.spec.ts` | 归一化纯函数、防抖、游标重置、工具栏搜索框、模型测试 |

## 验证

### 真实 PostgreSQL 集成（`-tags=integration`，容器 `evia-development-postgres-1:55432/evia_test`）

- `TestSavedAnalysisSearchFiltersByTitleSubstring`：中文子串 2 条、拉丁大小写不敏感 1 条；转义 `%`/`_` 只命中含该字面量的标题；无匹配空非 nil 页；staging 搜索不泄漏 dev 数据与基线行；空词返回全量 5 条。
- `TestSavedAnalysisSearchCombinesArchive`：两条同词分析归档其一后，活跃搜索减 1、归档搜索仍命中，证明归档谓词与搜索合取。

### 领域、HTTP 边界与客户端

- 共享 `TestNormalizeCatalogSearch`/`TestEscapeLikePatternTreatsMetaCharactersAsLiterals` 在改名后保持通过；保存分析服务 limit 边界单测同步九参签名。
- `TestSavedAnalysisHTTPListSearchTerm`：trim 后到达假服务、limit=1 游标只能在含词 scope 解码、未知参数 400、129 字词语音 400 且不到达服务；`TestMetricHTTPArchiveViewSelector` 新增指标列表拒 q 的 400 断言。
- api-client 全套 46 项通过（新增 1 项 q 编码，含中文/空格/`%`/`_` 字面量与空白省略）；codegen 后 wire 类型零变化、`schema.gen.ts` 仅增 `q?` 两行。
- Playwright 模型套件 99 项通过，新增保存分析搜索用例覆盖 trim、空白、128/129 rune、emoji 码点计数与内部空白保留。

### 变异红绿（sha256 逐字节还原，禁用 git checkout）

后端两轮（基线 `/tmp/evia-check/saved-search/SHA256SUMS`，3 文件）：

1. 存储谓词置 `($5='' OR TRUE)`（参数计数不变）→ 两个搜索集成测试同时变红。
2. 保存分析白名单移除 `q` → HTTP 搜索请求 400，边界测试变红。

前端两轮（基线 `/tmp/evia-check/saved-search-fe/SHA256SUMS`，4 文件）：

3. 客户端钳制截断改为 `maxSavedAnalysisSearchRunes + 1` → 新模型用例精确变红。
4. 移除页面 `search`/`onSearchChange` 接线 → tsc 报 TS2739 缺必填属性。

每轮还原后 `sha256sum -c` 全部 OK 并复跑复绿。

### 门禁

- `make check PYTHON=…/venv/bin/python` 退出码 0：首次命名检查在上一切片验证记录中检出第一方禁用术语本词（文档叙事引用），改述后通过；其后 prettier 对新模型测试长行重排，格式化后复跑通过。后端 `-race` 全量包与 Web（prettier、typecheck、test:client 46 项、SDK、生产构建）全绿。
- 全量 PG 集成包 `go test -race -tags=integration -count=1 -skip TestFailoverBaselineWrite ./internal/storage/postgres` 一次通过（240.3s，0 FAIL，无 flake）。

## 明确未做（边界）

- 原子指标与复合公式指标列表仍不接受 `q`；其白名单刻意保持不含搜索参数，留待后续同构切片按需开放，本切片不越界改动。
- 仅标题子串匹配，不做描述/字段全文检索，不引入索引或搜索引擎。
- 控制台无组件级单测设施，搜索交互以纯函数模型测试 + tsc 变异红绿 + 生产构建为证据。
