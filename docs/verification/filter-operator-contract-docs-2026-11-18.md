# 筛选算子契约自描述（filter op enum descriptions）验证记录（2026-11-18）

## 问题（确定性缺口）

`query-request.schema.json` 中筛选 DSL 的三个 `op` 枚举——逻辑节点（`and`/`or`）、标量比较（14 个算子）、一元存在性（`is_missing`/`is_present`）——都只有枚举值没有 `description`，而同类枚举（聚合算子、留存粒度、路径折叠、归因模型等）均带自描述。对依赖 OpenAPI/JSON Schema 自助接入的机器凭据与 Agent 调用方，这是正式 API 的契约完备性缺口：算子语义（尤其是守卫-反转、三值逻辑、绑定参数）只能从内部文档得知。

## 范围与语义决策

1. **纯文档、零行为变化**：只在三个 `op` 枚举上增加 `description`，不增删任何枚举值、不改校验/计划器/存储代码。描述内容全部对应已实现并经既有测试钉住的行为，不引入任何新语义或承诺。
2. **描述与实现逐条对齐**：逻辑节点说明「1–20 子节点、无通用 NOT，否定只通过显式 `not_*` 表达」；标量比较说明各算子适用类型、`not_*` 的守卫-反转（缺失键/显式 null/类型不符仍排除）、空 `not_in` 语义与绑定参数；一元算子说明 `is_present` 含显式 null、`is_missing` 仅缺失键、不做缺省强转。
3. **枚举值字节级不变**：通过 `git diff` 证明生成的 TS 仅新增 JSDoc、`op:` 联合类型行零增删；Go 离线 bundle 仅多描述字符串，枚举集合不变。
4. **不弱化 fail-closed**：未知算子仍被 JSON Schema 枚举与域 `Validate` 双重拒绝（既有 `planner_test` 的 `raw_sql` 未知算子负例与 `verify_contracts.py` 负例继续通过）。
5. **边界**：看板参数的独立算子白名单不在本次范围；本切片只覆盖分析查询筛选 DSL 的契约自描述。

## 改动清单

| 文件 | 说明 |
| --- | --- |
| `contracts/query/query-request.schema.json` | 三个 `op` 枚举补 `description`（逻辑/标量比较/一元） |
| `backend/internal/gen/wire/contracts.gen.json` | 重新生成的离线校验 bundle（仅描述字符串变化） |
| `packages/api-client/src/schema.gen.ts` | 重新生成的 TS 类型（仅 JSDoc 注释变化，联合类型不变） |

## 验证

### 等价性（无行为面，不做变异）

- `git diff packages/api-client/src/schema.gen.ts` 显示变更全部是 `/** @description ... */` JSDoc，`op:` 枚举联合行增删数为 0；Go bundle 同理仅新增描述。因此没有可被运行时变异影响的代码路径，强行做行为变异只会制造误导性弱变红，故本切片不设变异，改以「枚举值零变化 + 全量门禁」证明等价。
- 描述中的每条语义均由既有测试覆盖：守卫-反转见 `filter_negation`/`filter_suffix` 系列与真实 ClickHouse 集成；未知算子拒绝见 `planner_test`（`raw_sql`）；存在性语义见类型化筛选集成（missing/null/false/zero/array）。

### 门禁

- `verify_contracts.py` 通过（6 共享夹具、18 journey、5 系统属性、12 保存分析、64 负例、指标/公式契约全过）；`check_naming.py` 术语通过；`tsc --noEmit` 与 prettier 通过。
- 全量 `make check` EXIT=0、零 FAIL、Go 32 包、client 49/49、sdk 63/63、生产构建通过。

## 明确未做（边界）

- 不改任何枚举值、校验规则、SQL 生成或运行时行为；不新增算子。
- 不补看板参数白名单与 OpenAPI 内联参数枚举的描述（独立闭集，保持各自边界）。
- 漏斗「严格有序（无穿插）」模式仍是单一成员闭集，但其穿插判定属开放语义选择，未在本切片自行决策，需单独拍板。
