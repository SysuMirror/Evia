# 看板标题搜索验证记录（2026-11-18）

## 问题（确定性缺口）

看板列表此前只支持环境、`view=active|archived` 与 `folder_id` 三类闭集过滤，规划中「目录侧栏搜索」的主区部分一直空缺：看板数量增长后无法按标题定位。本切片交付列表 `q` 查询参数的完整垂直链路——领域归一化与 LIKE 转义、PostgreSQL 字面量子串谓词、HTTP 闭集与游标作用域、OpenAPI 契约与代码生成、手写客户端、控制台防抖搜索框——不引入任何新表或迁移。

## 范围与语义决策

1. **字面量匹配，元字符失效**：用户输入中的 `\`、`%`、`_` 经领域纯函数 `EscapeLikePattern` 前置反斜杠转义，存储 SQL 显式声明 `ESCAPE '\'` 并以 `'%' || $6 || '%'` 包裹，因此 `%`/`_` 永远按普通字符匹配，不能被构造成通配模式。
2. **空白与长度边界 fail-closed**：`NormalizeDashboardSearch` 先 `TrimSpace`，超过 128 rune 返回 `INVALID_QUERY`（指针 `/q`）；空白或省略等价于不过滤。看板标题本身上限 128 rune，更长的词不可能命中。
3. **谓词合取，不跨环境**：搜索谓词与项目、环境、归档视图、文件夹范围以 AND 组合，环境隔离仍由既有 `environment=$2` 兜底；空搜索以 `$6=''` 短路关闭，不改变原列表行为。
4. **游标与搜索词绑定**：handler 把归一化后的词（非转义词）追加进签名游标 scope，换词后旧游标解码因 scope 不符返回 `INVALID_CURSOR`，杜绝跨查询分页。handler 与 service 各做一次归一化，二者幂等，属纵深防御。
5. **控制台防抖与分页重置**：文本框即时值与 300ms 防抖提交值分离，只有提交值进入 react-query queryKey；提交词变化即重置游标到第一页。客户端镜像 128 rune 钳制，空白时省略 `q` 参数，使本地输入不可能触发必然 400。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 领域 | `reporting/dashboard_search.go`（新）、`dashboard_search_test.go`（新）、`dashboard.go`、`dashboard_service.go` | `NormalizeDashboardSearch`/`EscapeLikePattern` 纯函数、`DashboardStore`/`DashboardService` 列表签名加 `search`、服务归一化后转义下发 |
| 存储 | `postgres/dashboard.go`、`dashboard_search_integration_test.go`（新）及 3 个既有集成测试调用点 | `title ILIKE '%' || $6 || '%' ESCAPE '\'` 合取谓词、`$6=''` 关闭过滤、9 参签名同步 |
| 契约/HTTP | `openapi.json`、生成 `schema.gen.ts`、`transport/control/reporting_saved.go`、`dashboard.go`、`dashboard_test.go` | `q` 入白名单与 OpenAPI（maxLength 128）、游标 scope 追加搜索段、未知参数仍 400、超长词不到达服务 |
| api-client | `packages/api-client/src/index.ts`、`index.test.ts` | `CatalogPage`/`DashboardPage` 增 `search?`、`catalogQuery` 编码 `q`、新增编码测试（中文/空格/`%`/`_` 字面量） |
| 控制台 | `dashboards/model.ts`、`use-history.ts`、`list.tsx`、`styles.css`、`dashboards.tsx`、`tests/browser/dashboard-model.spec.ts` | 客户端归一化纯函数、300ms 防抖、换词重置游标、工具栏搜索框、模型测试 |

## 验证

### 真实 PostgreSQL 集成（`-tags=integration`，容器 `evia-development-postgres-1:55432/evia_test`）

- `TestDashboardSearchFiltersByTitleSubstring`：中文子串命中 2 条、拉丁大小写不敏感命中 1 条；转义 `%` 仅命中含百分号标题、转义 `_` 仅命中含下划线标题（证明元字符字面量化）；无匹配返回空非 nil 页；staging 环境搜索 dev 数据为 0（不跨环境）；空词返回全量。
- `TestDashboardSearchCombinesFolderAndArchive`：搜索×文件夹交集只返回文件夹内命中；归档其中一条后活跃搜索减 1、归档视图同文件夹搜索仍能命中，证明三个谓词同时生效。

### 领域、HTTP 边界与客户端

- 领域 `TestNormalizeDashboardSearch`（trim、空白、128/129 rune 边界）与 `TestEscapeLikePatternTreatsMetaCharactersAsLiterals`（三类元字符转义、普通字符不膨胀）通过。
- `TestDashboardHTTPListSearchTerm`：带空白的中文词被 trim 后到达假服务；limit=1 产生的 next_cursor 只能在含搜索词的 scope 下解码；夹带未知参数 400；129 字词语音 400 且服务调用计数不增。既有游标测试的 scope 字符串同步为双冒号尾段。
- api-client 全套 45 项通过（新增 1 项 q 编码）；codegen 后 wire 类型零变化、`schema.gen.ts` 仅增 `q?` 两行，`generated-check` 语义由 `make check` 覆盖。
- Playwright 模型套件 98 项通过，其中 `dashboard-model.spec.ts` 8 项；新增用例覆盖 trim、空白归一、128 rune 临界、129 rune 钳制、emoji 按码点计数与内部空白词法保留。

### 变异红绿（sha256 逐字节还原，禁用 git checkout）

后端三轮（基线 `/tmp/evia-check/dash-search/SHA256SUMS`，5 文件）：

1. 存储谓词置 `($6='' OR TRUE)`（保持参数计数不变）→ 子串搜索返回 5 条而非 2 条，集成测试变红。
2. `EscapeLikePattern` 移除 `%`/`_` 转义 → 领域单测变红。
3. 白名单移除 `q` → HTTP 搜索请求 400，边界测试变红。

前端两轮（基线 `/tmp/evia-check/dash-search-fe/SHA256SUMS`，4 文件）：

4. 客户端钳制截断改为 `maxDashboardSearchRunes + 1` → 新模型用例精确变红（其余 7 项仍绿）。
5. 移除页面装配的 `search`/`onSearchChange` 接线 → tsc 报 TS2739 缺必填属性。

每轮还原后 `sha256sum -c` 全部 OK 并复跑复绿。

### 门禁

- `make check PYTHON=…/venv/bin/python` 退出码 0：命名（首次因注释出现禁用词 "tracks" 失败，改述后通过）、契约、边界、监控、后端 `-race` 全量包与 Web（prettier、typecheck、test:client 45 项、SDK、生产构建）全绿。
- 全量 PG 集成包 `go test -race -tags=integration -count=1 -skip TestFailoverBaselineWrite ./internal/storage/postgres` 一次通过（242.9s，0 FAIL，无 flake）；`TestFailoverBaselineWrite` 属既有多主机 DSN 门控，未设置 `EVIA_TEST_PG_FAILOVER_DSN` 时跳过。

## 明确未做（边界）

- 仅支持标题子串搜索；不做描述/组件名/全文检索，不引入搜索引擎或物化索引，ILIKE 前缀通配无法走普通 btree 索引，依赖列表分页的有界结果集。
- 不做搜索词服务端持久化或跨设备同步；防抖窗口与分页重置完全是客户端行为。
- 控制台无组件级单测设施，搜索交互以纯函数模型测试 + tsc 变异红绿 + 生产构建为证据；端到端浏览器联调沿用既有看板工作流。
- 目录侧栏内复制入口、高级图表配置仍属后续切片，与本能力正交。
