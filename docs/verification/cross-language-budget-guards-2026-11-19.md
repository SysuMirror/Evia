# 导出预算跨语言契约锚点守卫验证记录（2026-11-19）

本切片为纯测试/可见性变更：无迁移、无契约内容变更、无任何运行时行为变化（生产代码唯一改动是给一个 TS 常量补 `export` 修饰符）。

## 问题（确定性缺口）

导出链路上的三个数值预算在系统中各有**三份独立副本**，此前没有任何机械对齐把它们绑定：

| 预算 | 契约数值约束（规范锚点） | Go 执法常量与执法点 | 控制台 TS 预提交常量与执法点 |
| --- | --- | --- | --- |
| 导出名称长度 200 | `openapi.json` 的 `ExportInput.name.maxLength`、`SavedAnalysisExportInput.name.maxLength` | `exporting.MaxNameLength`（`exporting.go:35`，`service.go:55,88` 两处入队校验） | `EXPORT_NAME_MAX`（`exports/model.ts:16`，`:51`）、`SAVED_EXPORT_NAME_MAX`（`saved-analysis-run/export-model.ts:12`，`:30`） |
| 结果行数上限 500 | `query-request.schema.json` 的 `properties.limit.maximum`（minimum 1） | `analysis.BudgetResultRows`（`budgets.go:15`，`validation.go:116`） | `EXPORT_LIMIT_MAX`（`exports/model.ts:18`，`:61`） |
| 事件标识上限 20 | `query-request.schema.json` 的 `properties.event_keys.maxItems`（uniqueItems） | `analysis.BudgetEventSelections`（`budgets.go:12`，`validation.go:122,539`） | `MAX_EVENT_KEYS`（`exports/model.ts:20`，`:66`） |

既有的 33 个契约守卫（见 [contract-alignment-guards](contract-alignment-guards-2026-11-19.md)）全部是 **Go 单语言内**「字符串枚举闭集 vs 嵌入 bundle」的对齐；#33 虽是数值型守卫，对齐的也是 Go 侧广告预算与 Go 侧执行常量。**控制台 TypeScript 副本与契约之间没有任何守卫**：任一侧把 500/20/200 改成别的值，漂移只能等到用户在控制台预检通过、提交后被服务端 400 拒绝（或反向放宽）时才在运行时暴露。

## 方案（双侧锚点，契约为共同规范源）

1. **Go 侧**新增 `exporting.TestExportBudgetsMatchContract`：读嵌入的 `wire.Schemas` bundle，用 `maxLength`/`maximum`/`maxItems` 三个闭包从 `$defs` 提取契约数值，断言四个等式——两个导出 schema 的 `name.maxLength == MaxNameLength`、`QueryRequest.limit.maximum == analysis.BudgetResultRows`、`QueryRequest.event_keys.maxItems == analysis.BudgetEventSelections`。
2. **TS 侧**：把 `exports/model.ts` 的 `MAX_EVENT_KEYS` 由模块私有改为 `export`（仅可见性变化，值与引用点不变），在 `tests/browser/export-model.spec.ts` 新增 `console export limits stay pinned to the canonical contracts`：借助根 tsconfig 的 `resolveJsonModule` 直接 import `contracts/query/query-request.schema.json` 与 `contracts/http/openapi.json` 两个原始契约文件，断言四个 TS 常量分别等于契约数值。
3. 不引入代码生成的运行时常量：`schema.gen.ts` 只导出类型、无运行时值，无法承担运行时断言；直接 import 契约 JSON 是当前工程形状下唯一的 TS 侧锚点路径，契约本身的手工/生成漂移继续由 `contracts-check`、`generated-check` 门禁拦截。

两侧守卫职责对称、互补：Go 守卫捕获「Go 常量 vs 契约」漂移，TS 守卫捕获「TS 常量 vs 契约」漂移，契约是双方共同的、可机器读取的规范源。

## 守卫有效性取证（七个断言分支独立变异红绿）

变异前对四个被改文件做干净快照（`/tmp/evia-check/xlang/snapshot/`），每个分支独立变异、独立取证、立即从快照还原，最后 `diff -q` 逐文件确认零残留。

Go 侧（`go test ./internal/exporting -run TestExportBudgetsMatchContract`）：

| 变异 | 红灯证据（精确报错） |
| --- | --- |
| `MaxNameLength` 200→199 | `budget_contract_test.go:73: ExportInput.name maxLength=200 != exporting.MaxNameLength=199` |
| `BudgetResultRows` 500→499 | `budget_contract_test.go:79: QueryRequest.limit maximum=500 != analysis.BudgetResultRows=499` |
| `BudgetEventSelections` 20→19 | `budget_contract_test.go:82: QueryRequest.event_keys maxItems=20 != analysis.BudgetEventSelections=19` |

TS 侧（Playwright `export-model` 套件，基线 17/17）：

| 变异 | 红灯证据（精确报错） | 结果 |
| --- | --- | --- |
| `EXPORT_LIMIT_MAX` 500→499 | `spec.ts:209 Expected: 500 Received: 499`；同文件既有的 `limit must stay inside the bounded 1-500 range`（`:68`）同步变红 | 2 failed / 15 passed |
| `MAX_EVENT_KEYS` 20→19 | `spec.ts:210 Expected: 20 Received: 19` | 1 failed / 16 passed |
| `EXPORT_NAME_MAX` 200→199 | `spec.ts:222 Expected: 200 Received: 199` | 1 failed / 16 passed |
| `SAVED_EXPORT_NAME_MAX` 200→199 | `spec.ts:223 Expected: 200 Received: 199` | 1 failed / 16 passed |

七次变异全部按预期变红，还原后双侧复绿；`grep "= 499\|= 199\|= 19$"` 确认工作区零变异残留，早期一轮变异的基线亦留存于 `/tmp/evia-check/xlang/baseline/`。

## 验证

- 全量 `make check` EXIT=0、零 FAIL：Go 33 包 ok（`go test -race ./...`，含新增守卫所在的 exporting 包）、api-client 51、browser-sdk 63，prettier/typecheck/生产构建全过。日志：`/tmp/evia-check/xlang/make-check.log`。
- Playwright `export-model` 套件 17/17 通过（约 0.8s，含新增守卫用例）；web-check 不包含 Playwright，该套件需独立运行。
- `gofmt -l` 对新 Go 文件零输出；`prettier --check` 对三个 TS 文件全部通过。切片中途曾因手写 spec 触发 prettier 失败，已 `prettier --write` 重排后重跑门禁确认。
- 无需真实依赖回归：无迁移、无 SQL/契约/网络面变更，生产逻辑零改动（仅一个 `export` 修饰符），Go 守卫读嵌入 bundle、TS 守卫读仓库内契约 JSON，均零外部依赖。

## 明确未做（边界）

- 不修改任何预算数值、契约内容或运行时校验行为；控制台与服务端的实际执法代码一行未动。
- 未广告的内部限额（路径深度、请求体字节等）按 [contract-alignment-guards](contract-alignment-guards-2026-11-19.md) 既有刻意边界不纳入，本切片只覆盖已在契约中携带数值约束、且双侧都有副本的三个导出预算。
- 孤立的 `AuditEntry`/`AuditList` schema（result allowed/denied/failed + request_id，零端点引用）仍不实现：其落地需要先决策 denied/failed 记录口径与 request_id 传播语义，属独立特性而非防漂移工程。
- 未把 TS 常量改为从单一生成物导入；当前以「测试钉扎 + 契约门禁」保证漂移必红，避免为三个常量引入代码生成链路。
