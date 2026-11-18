# 查询契约系统字段 key 枚举自描述（system-field key descriptions）验证记录（2026-11-18）

## 问题（确定性缺口）

继筛选 DSL 三个 `op` 枚举与查询顶层枚举自描述之后，`query-request.schema.json` 中仍有 7 处内联的系统字段 `key` 枚举只有值没有 `description`：`group_by`、`metrics.field`、漏斗 `same_entity.field`、共享 `$defs.field`、`$defs.segment_field`，以及 `$defs.filter` 的两个变体。对依赖 OpenAPI/JSON Schema 自助接入的机器凭据与 Agent，以下约束只能从内部文档或代码得知：内置系统列与事件作用域自定义属性（`kind=property`）的区分、七个固定列各自的语义、`event_time` 是唯一 datetime 系统字段、以及各列 presence/空串守卫并不相同。这是正式 API 契约完备性的最后一块枚举缺口。

## 范围与语义决策

1. **纯文档、零行为变化**：只在 7 个内联枚举上增加同一条 `description`，不增删枚举值、不改校验/计划器/存储/控制台代码。每条表述均对应已实现并经测试钉住的行为。
2. **逐句的代码依据**：
   - 七个固定列与类型来自 `backend/internal/analysis/validation.go:21` 的 `systemTypes`：仅 `event_time` 为 `datetime`，其余六列为 `string`；未知 key 在 `Validate` 与 planner `field()` 两处被 `INVALID_FIELD` 拒绝（双重 fail-closed）。
   - 系统字段必须是全局列：`validateField` 要求系统字段 `EventKey==""`、`SchemaVersion==0`；自定义属性则必须携带事件与已发布版本，即描述中「event-scoped key」的含义。
   - presence 守卫因列而异：`backend/internal/analysis/planner.go:84-87` 对 `event_key`/`event_time` 取恒真 `present=1`，其余五列渲染 `(isNotNull(x) AND x!='')`，与描述「Presence and empty-string guards differ by column」一致。
   - `$defs.segment_field` 的枚举只有 6 值（刻意排除 `event_time`）：分群标签必须是标量，`backend/internal/analysis/fields.go:207-211` 对 datetime/数组显式返回 `INVALID_SEGMENT_FIELD`，schema 闭集与域守卫对齐。
   - `uid`/`anonymous_id` 是统计主体标识（与 `subject=uid|anonymous` 对应），`session_id`/`page_view_id`/`operation_id` 是执行上下文列，`event_key` 是事件类型，命名与 planner 直接以 key 作为物理列名的事实一致。
3. **枚举值字节级不变**：7 个块的值数量保持 7/7/7/7/6/7/7；对源 schema 与 Go 离线 bundle 分别做与 HEAD 的结构化递归对比，差异仅为新增 `description` 叶；生成的 TS 只有 JSDoc 增行，枚举联合行零增删。
4. **不弱化 fail-closed**：未知系统字段 key 仍被 JSON Schema 枚举与域校验双重拒绝，既有负例夹具继续通过。
5. **边界**：看板参数的独立白名单是另一资源的闭集，不在本切片范围，避免跨资源扩面。

## 改动清单

| 文件 | 说明 |
| --- | --- |
| `contracts/query/query-request.schema.json` | 7 处内联系统字段 `key` 枚举补统一 `description` |
| `backend/internal/gen/wire/contracts.gen.json` | 重新生成的离线校验 bundle（仅 7 个描述字符串叶） |
| `packages/api-client/src/schema.gen.ts` | 重新生成的 TS 类型（仅 JSDoc，联合类型不变） |

## 验证

### 等价性（无行为面，不做变异）

- 源 schema 解析确认 7 个块均为 `{description, enum}` 结构，枚举值数量与内容相对 HEAD 零变化，键集合仅多 `description`。
- Go bundle 与 HEAD 的递归结构对比（含数组逐元素）：恰好 7 处 `added ...key.description`，无任何值、枚举、数组长度或结构变化。
- `git diff packages/api-client/src/schema.gen.ts`：75 行变更全部为 `/** @description ... */` JSDoc；含 `event_key` 等枚举字面量的联合类型行增删数为 0。
- 本切片没有任何运行时代码面，强行变异只会产生误导性弱变红，故以「枚举零变化 + 全量门禁」证明等价，与前两个自描述切片口径一致。

### 门禁

- `check_naming.py`、`verify_contracts.py`（含 64 保存分析负例、指标/公式契约）、`tsc --noEmit`、prettier、client 单测 49/49 均通过。
- 全量 `make check` EXIT=0、零 FAIL：Go 32 包 ok、client 49/49、sdk 63/63、console 18/18、生产构建通过。日志：`/tmp/evia-check/system-field-contract-docs/make-check.log`。

## 明确未做（边界）

- 不改任何枚举值、校验规则、SQL 生成或运行时行为；不新增查询类型/算子/字段。
- 不补看板参数白名单的描述（独立资源闭集，另案处理）。
- 漏斗「严格有序」模式、身份关联图（`resolved_person` 仍 fail-closed）、来源系统字段（source_id/source_kind，属 catalog 受管面）与外部基础设施（多机房 TLS、parquet、对象存储、SMTP）仍是需单独拍板的语义/产品决策，本切片不自行决定。
