# 分析报告标题搜索验证记录（2026-11-18）

## 问题（确定性缺口）

看板、保存分析、原子指标与复合公式指标列表已交付统一的 `q` 标题搜索契约，分析报告列表仍只支持必填 `environment` 与 `limit`/`cursor` 分页。本切片把同一契约复用到报告列表，至此五类资源（看板、保存分析、原子指标、复合公式指标、分析报告）的列表标题搜索全部对齐，不引入新表或迁移。

## 范围与语义决策

1. **reports 包保持精简独立**：reports 是只依赖 access/fault 的独立领域包，刻意不反向依赖重型 reporting bounded context。归一化与 LIKE 转义由包内新增的 `NormalizeTitleSearch`/`EscapeLikePattern`（`internal/reports/search.go`）本地实现，而不是复用 reporting 包的 `NormalizeCatalogSearch`；两者语义同构但错误码沿用 reports 自己的 `INVALID_REPORT_INPUT`，超长词消息为「搜索关键词不能超过 128 个字符」。
2. **服务层单点归一化+转义**：HTTP handler 先 `NormalizeTitleSearch` 取得归一化词（用于游标 scope），把未转义词下发给 `Service.List`；服务层再次幂等归一化后只做一次 `EscapeLikePattern`，存储只接收已转义字面量，不存在双重转义。
3. **字面量匹配与游标绑定**：存储以 `($5='' OR title ILIKE '%' || $5 || '%' ESCAPE '\')` 合取谓词匹配，请求文本只作为绑定参数、永不拼接进 SQL；handler 把归一化词追加进签名游标 scope（`reports:actor:project:env:term`），跨词游标报 `INVALID_CURSOR`。
4. **白名单闭集**：报告列表走 `governanceQuery(r, "environment", "limit", "cursor", "q")`，未知参数依旧 fail-closed，`q` 不改变既有未知参数拒绝行为。
5. **控制台同构防抖**：沿用 300ms 防抖、即时值/提交值分离、提交词变化重置游标、空白省略 `q`、128 rune 客户端钳制；搜索框放在 `Toolbar` 的 `actions` 槽，不新增包裹容器。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 领域 | `internal/reports/search.go`（新）、`search_test.go`（新）、`reports.go`、`reports_test.go` | 包内归一化+转义 helper、Store/Service.List 加 `search`、假存储签名同步 |
| 存储 | `postgres/reports.go`、`report_search_integration_test.go`（新）、`reports_integration_test.go` | `title ILIKE ... ESCAPE '\'` 合取谓词、`$5=''` 关闭过滤、既有调用点补参 |
| 契约/HTTP | `openapi.json`、生成 `schema.gen.ts`、`transport/control/reports.go`、`reports_test.go` | `governanceQuery` 白名单加 `q`、游标 scope 加搜索段、未知参数/超长词 400、硬编码 scope 同步尾冒号 |
| api-client | `packages/api-client/src/index.ts`、`index.test.ts` | `listReports` 页参数加 `search?`、复用 `catalogQuery` 的 q 编码、新增编码测试 |
| 控制台 | `reports/model.ts`、`reports.tsx`、`styles.css`、`tests/browser/reports-model.spec.ts` | 归一化纯函数、防抖、游标重置、工具栏搜索框、模型测试 |

## 验证

### 真实 PostgreSQL 集成（`-tags=integration`，容器 `evia-development-postgres-1:55432/evia_test`）

- `TestReportSearchFiltersByTitleSubstring`（verbose 复跑 0.65s PASS）：夹具基线 + 4 个目标，空词返回全量 5 条；中文子串「留存」命中 2 条且不含无关报告；拉丁 `revenue` 大小写不敏感命中 1 条；转义 `%` 只命中 `Revenue 100%match`、转义 `_` 只命中 `DAU_趋势`，证明元字符按字面量而非通配符；无匹配词返回空非 nil 页；staging 搜索为空且不泄露 development 行。
- 对 store 直接传 `EscapeLikePattern(term)`、对 service 传原始词的分工在测试中显式注释，避免双重转义。

### 领域、HTTP 边界与客户端

- 领域 `TestNormalizeTitleSearch`、`TestEscapeLikePatternTreatsMetaCharactersAsLiterals` verbose PASS：trim、129 rune 拒绝、`` `a%_\b` `` → `a\%\_\\b` 字面量转义。
- HTTP `TestReportListSearchTerm` verbose PASS（0.17s）：trim 后「周报」到达假服务、limit=1 游标只能在含词 scope 解码、未知参数 `unexpected=1` 仍 400、129 字词语音 400 且不到达服务（服务状态不变）；硬编码 scope 字符串同步为尾冒号形式。
- api-client 全套 49 项通过（新增 `report search term encodes as q and omits when blank`，覆盖中文空格 `+`、`%`→`%25`、空白省略 q 三种编码形态）；codegen 后 wire 类型零变化、`schema.gen.ts` 仅增 `q?` 两行。
- Playwright 模型套件 102 项通过，reports 规格 6 项含新增搜索用例（trim、空白、128/129 rune、emoji 码点计数、内部空白保留）。

### 变异红绿（sha256 逐字节还原，禁用 git checkout）

后端两轮（基线 `/tmp/evia-check/report-search/SHA256SUMS`，3 文件）：

1. 存储谓词置 `($5='' OR TRUE)` → `TestReportSearchFiltersByTitleSubstring` 变红（子串过滤失效、全量返回）。
2. `governanceQuery` 白名单移除 `q` → `TestReportListSearchTerm` 变红（带 q 请求被 400 拒绝）。

前端两轮（基线 `/tmp/evia-check/report-search-fe/SHA256SUMS`，3 文件）：

3. 客户端钳制截断改为 `maxReportSearchRunes + 1` → 新模型用例精确变红（期望 128、实得 129）。
4. 移除 `normalizeReportSearch` 导入但保留调用 → tsc 报 TS2304（reports.tsx:334）。

每轮还原后 `sha256sum -c` 全部 OK 并复跑复绿；前端最终 tsc `--noEmit` 与 102 项模型测试再次通过。

### 门禁

- `make check PYTHON=…/venv/bin/python` 一次通过（EXIT=0）：命名、契约、边界、监控、Python 工具、gofmt、`go vet`、后端 `-race` 全量包、prettier、typecheck、test:client 49 项、SDK 57 项、SDK 与控制台生产构建全绿（vite build 321ms）。
- 全量 PG 集成包 `go test -race -tags=integration -count=1 -skip TestFailoverBaselineWrite ./internal/storage/postgres` 一次通过（242.1s，EXIT=0，0 FAIL；`TestFailoverBaselineWrite` 需独立多主机 `EVIA_TEST_PG_FAILOVER_DSN`，属既有环境门控，干净基线同样跳过）。

## 明确未做（边界）

- 仅标题子串匹配，不做描述/章节全文检索，不引入索引或搜索引擎。
- 报告无归档/视图维度，搜索只与环境谓词合取，不存在 `view` 参数。
- 控制台无组件级单测设施，搜索交互以纯函数模型测试 + tsc 变异红绿 + 生产构建为证据。
- 报告资源的其余既有规划限制（章节仅引用已发布保存分析、环境内资源、不执行查询）保持原状。
