# 看板 Markdown 文本卡验证记录（2026-11-17）

## 问题（确定性缺口）

看板组件此前只能固定可执行来源（保存分析版本或指标版本），缺少纯展示能力：作者无法在数据看板内放置口径说明、阅读指引或注释段落。规划文档要求「Markdown 文本卡仅允许净化后的有限标记，禁止脚本、iframe 和任意 HTML」。本切片交付第三种组件来源 `text_card` 的领域模型、数据库门控、HTTP 契约、客户端与控制台闭环，文本卡全程不产生查询。

## 范围与语义决策

1. **三选一互斥**：每个 widget 必须且只能持有 analysis 版本对、metric_ref 或 text_card 之一；领域归一化、OpenAPI 描述与迁移 038 的 PL/pgSQL 形状函数三层一致拒绝「无源」或「多源」。
2. **受限 Markdown 子集，fail-closed**：`content` 为 1–4096 rune；拒绝原始 HTML/注释/doctype、图片语法、`javascript:`/`vbscript:`/`data:` 协议与未闭合围栏代码块；链接目标必须匹配 `^https?://[^\s<>"]+$`。支持标题、段落、加粗/斜体（仅星号，下划线按字面量处理以兼容 snake_case）、行内代码、有序/无序列表、引用、围栏代码块与安全链接。
3. **无查询副作用**：文本卡不写 `dashboard_dependencies` 见证、不进 `dashboard_run_widgets`、不入查询队列，仅存在于 definition JSON（仍受 64KiB 总包络与形状 CHECK 约束）；禁止 `parameter_bindings`（归一化与 SQL 门控双重拒绝）。
4. **纯文本卡看板**：可正常发布，依赖清单 `analyses` 必须序列化为 `[]` 而非 `null`；运行入队在快照读取后显式返回 `DASHBOARD_NO_EXECUTABLE_WIDGET`，避免零值 `expires_at` 撞 `dashboard_runs` 的 `CHECK(expires_at>created_at)` 而暴露 500 类内部错误，且不残留幽灵运行。
5. **控制台渲染安全**：自建纯 TS/React 子集渲染器，所有文本经 React 子节点自动转义，全链路无 `dangerouslySetInnerHTML`、无第三方 Markdown 库；链接在渲染侧再次独立校验 http(s)，未闭合围栏降级为代码块而不吞掉后续内容。
6. **不可变版本即事实**：文本卡内容随发布版本固化，运行页直接从版本 definition 渲染，不依赖任何运行态数据。

## 改动清单

| 层 | 提交 | 文件 | 说明 |
| --- | --- | --- | --- |
| 领域 | `b743120` | `reporting/text_card.go`、`text_card_test.go`、`dashboard_policy.go`、`dashboard.go` | `TextCard` 模型、`ValidateTextCardMarkdown`、三选一归一化、克隆深拷贝、校验依赖计数只算可执行组件 |
| 存储 | `e1ba37a` | 迁移 `038_dashboard_text_cards.sql`、`postgres/dashboard_runs.go`、集成测试 | 替换形状函数与 deferred trigger（基于 034 双目标版加文本卡过滤）、扇出按索引零值跳过、运行行/依赖行跳过 |
| 契约 | `1881408` | `openapi.json`、生成 wire/TS、`transport/control/dashboard.go`、api-client | `DashboardTextCard` schema、边界 `ValidateDashboardTextCards` 在 create/save dispatch 前 fail-closed、客户端类型与载荷测试（43 项） |
| 领域修复 | `b80435e` | `reporting/dashboard_policy.go`、发布集成测试 | 零见证时非空切片种子，保证 `analyses` 恒为 `[]` |
| 存储门控 | `7136b31` | `postgres/dashboard_runs.go`、集成测试 | `DASHBOARD_NO_EXECUTABLE_WIDGET` 显式拒绝与零残留断言 |
| 控制台基础 | `f9ac6fe` | `dashboards/text-card.tsx`、`model.ts`、`types.ts`、`styles.css`、模型测试 | 安全渲染器/块解析、三选一断言、rune 边界、联合类型扩展 |
| 控制台接线 | `3462fd5` | `picker.tsx`、`editor.tsx`、`run.tsx`、`runtime-filters.tsx` | 文本卡来源选择与 Textarea、编辑实时预览、运行页渲染、纯文本卡禁运、筛选器排除 |

## 验证

### 真实 PostgreSQL 集成（`-tags=integration`，容器 `evia-development-postgres-1:55432/evia_test`）

- `TestDashboardTextCardShapeGate`：合法文本卡通过 SQL 门控；HTML、analysis+text_card 组合、参数绑定、javascript 协议、未闭合围栏、无源六类形状全部拒绝。
- `TestDashboardTextCardPublishAndRunFanout`：混合看板仅 1 条依赖见证、运行仅扇出 1 个可执行组件、`dashboard_run_widgets` 恰 1 行，文本卡内容在不可变版本中可读。
- `TestDashboardTextCardOnlyPublication`：纯文本卡看板发布成功、零见证、空清单为数组形状、版本内文本卡存活。
- `TestDashboardTextCardOnlyRunRejected`：纯文本卡入队返回 `DASHBOARD_NO_EXECUTABLE_WIDGET`，`dashboard_runs` 零残留。

### 领域、HTTP 边界与客户端

- `reporting` 包文本卡单测覆盖安全/合法 Markdown 样本；`TestDashboardHTTPTextCardContracts` 覆盖 create 与 save 双路径：合法 Markdown 放行且各 dispatch 一次，script/javascript/图片三类在边界 400 且不到达假服务。
- api-client 新增 draft 载荷传输测试，全套 43 项通过；`verify_contracts.py` 契约校验通过。
- Playwright 模型套件 96 项通过，含新增三选一不变量（无源/多源/空内容/超长/参数绑定）与 Markdown 块结构解析（标题/段落/引用/双列表/围栏/未闭合降级）。

### 变异红绿（sha256 逐字节还原，禁用 git checkout）

1. 契约层（基线 `/tmp/evia-check/text-card-contract/SHA256SUMS`）：create 守卫置恒假 → 危险 POST 返回 201 变红；save 守卫置恒假 → 危险 PATCH 返回 200 变红；`ValidateDashboardTextCards` 短路返回 nil → 双路径变红。三轮还原后 `sha256sum -c` 全 OK。
2. 运行门控（基线 `/tmp/evia-check/text-card-run/SHA256SUMS`）：移除零可执行组件守卫 → 退化为 PG 原始 `dashboard_runs_check2` 约束错误（证实守卫把 500 转为领域错误）；nil 切片种子复原 → 发布期 manifest scope 拒绝复现。还原哈希一致。
3. 控制台类型（基线 `/tmp/evia-check/text-card-console/SHA256SUMS`）：联合类型删除 text 成员 → tsc 在 picker 提交点（TS2322）与 editor 收窄点（TS2339 never）同时变红，还原后哈希一致。
4. 控制台模型/解析：标题正则改为仅匹配 4–6 级 → 解析块序列断言变红；三选一计数退回二选一 → 合法文本卡被误报变红。均还原复绿。

### 门禁

- `make check PYTHON=…/venv/bin/python` 退出码 0：命名、契约、边界、监控、后端 `-race` 全量包与 Web（typecheck、prettier、build）全绿；codegen 幂等无 diff。
- 全量 PG 集成包 `go test -race -tags=integration -skip TestFailoverBaselineWrite ./internal/storage/postgres` 通过（236.6s，0 FAIL）。`TestFailoverBaselineWrite` 属既有多主机 DSN 门控，与本切片无关；期间一次 `TestExhaustedIPCannotAllocateAccountBuckets` 连接重置 flake 隔离复跑通过、全量复跑干净。

## 明确未做（边界）

- 不支持图片、表格、原始 HTML、嵌套混合列表与任意 URL 协议；服务端 fail-closed 列表是能力边界而非临时限制，扩展子集需同时改 Go 校验、SQL 形状函数与控制台渲染器。
- 文本卡不参与参数绑定、运行筛选、比较期与任何查询预算；纯文本卡看板没有运行入口（按钮禁用并提示）。
- 控制台无浏览器级组件测试设施，渲染器证据为 Playwright 模型级解析断言 + tsc 变异红绿 + 生产构建；端到端浏览器联调沿用既有看板工作流，未新增专用 spec。
- 跨环境复制重绑定、高级图表配置仍属后续切片，与本能力正交。
