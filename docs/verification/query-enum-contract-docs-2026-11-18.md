# 查询契约顶层枚举自描述（query top-level enum descriptions）验证记录（2026-11-18）

## 问题（确定性缺口）

继筛选算子枚举自描述之后，`query-request.schema.json` 的顶层查询枚举仍只有值没有 `description`：查询 `kind`（8 种）、`environment`、`subject`、聚合 `accuracy`、趋势 `interval`。对依赖 OpenAPI/JSON Schema 自助接入的机器凭据与 Agent，这是正式 API 的契约完备性缺口——尤其 `resolved_person` 保留但 fail-closed、`approximate` 仅适用部分算子、`week` 周一起算且无 `month` 等约束，调用方只能从内部文档得知。

## 范围与语义决策

1. **纯文档、零行为变化**：只在 5 个顶层枚举上增加 `description`，不增删枚举值、不改校验/计划器/存储/控制台代码。每条描述对应已实现并经测试钉住的行为。
2. **逐枚举的实现依据**：
   - `kind`：8 种引擎与各自配置块，描述与 `Validate` 的分派及 planner 各 case 一致；journey 需单一身份过滤已在 `identity_filter` 校验中强制。
   - `environment`：限定事实发布环境，机器凭据须与授权环境匹配（machine grant 校验）。
   - `subject`：uid 原始 UID、anonymous 原始匿名设备标识（非自然人）；`resolved_person` 在 `validation.go` 两处显式返回 `IDENTITY_RESOLUTION_UNAVAILABLE`（fail-closed），描述与代码一致。
   - `accuracy`：默认 exact；approximate 仅 `unique` 与 p50/p90/p95/p99 接受，其他算子拒绝（既有 `TestMetricPrecision...` 与 planner 精度守卫覆盖）。
   - `interval`：趋势桶，省略即整段聚合；week 用 `toStartOfWeek` mode 1 周一起算；刻意无变长 month（趋势 31 天窗口下低价值且破坏固定桶宽）。
3. **枚举值字节级不变**：`git diff` 证明生成的 TS 仅新增 JSDoc，`kind/environment/subject/interval/accuracy` 的枚举联合行增删数为 0；Go 离线 bundle 仅多描述字符串。
4. **不弱化 fail-closed**：未知 kind/environment/subject/interval/accuracy 仍被 JSON Schema 枚举与域 `Validate` 双重拒绝，既有负例继续通过。
5. **边界**：系统字段 `key` 枚举（固定列名、自描述且在 schema 中重复 6 次）与看板参数独立白名单不在本次范围，避免噪声与跨资源扩面。

## 改动清单

| 文件 | 说明 |
| --- | --- |
| `contracts/query/query-request.schema.json` | 5 个顶层枚举补 `description` |
| `backend/internal/gen/wire/contracts.gen.json` | 重新生成的离线校验 bundle（仅描述字符串） |
| `packages/api-client/src/schema.gen.ts` | 重新生成的 TS 类型（仅 JSDoc，联合类型不变） |

## 验证

### 等价性（无行为面，不做变异）

- `git diff packages/api-client/src/schema.gen.ts` 全部变更为 `/** @description ... */` JSDoc，枚举联合行增删数为 0；Go bundle 同理。无可被运行时变异影响的代码路径，强行变异只产生误导性弱变红，故以「枚举零变化 + 全量门禁」证明等价。
- 描述中的每条约束均由既有测试覆盖：`resolved_person` fail-closed、approximate 适用面、interval 周一起算、各 kind 的配置校验，均有对应单元/集成测试与近期切片的真实 ClickHouse 证据。
- 过程中发现并修正了首版 `kind` 描述对 journey 的重复表述，保证每个引擎只描述一次。

### 门禁

- `verify_contracts.py`（含 64 保存分析负例、指标/公式契约）、`check_naming.py`、`tsc --noEmit`、prettier 均通过。
- 全量 `make check` EXIT=0、零 FAIL、Go 32 包、client 49/49、sdk 63/63、生产构建通过。

## 明确未做（边界）

- 不改任何枚举值、校验规则、SQL 生成或运行时行为；不新增查询类型/算子/字段。
- 不补系统字段 key 与看板参数白名单的描述（独立闭集/重复噪声）。
- 漏斗「严格有序」模式、来源系统字段（source_id/source_kind，属 catalog 受管面）与身份图仍是需单独拍板的语义/产品决策，本切片不自行决定。
