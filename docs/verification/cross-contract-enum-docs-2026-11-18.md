# 跨契约枚举自描述（cross-contract enum descriptions）验证记录（2026-11-18）

## 问题（确定性缺口）

查询契约三轮自描述（筛选 `op`、顶层枚举、系统字段 `key`）之后，其余五个公开 JSON Schema 仍有 9 个只有值、没有 `description` 的裸枚举，机器凭据与 Agent 接入方无法直接从契约读出语义：

- `event-envelope.schema.json`：`context.navigation_type`；
- `event-plan.schema.json`：`status`、`environment`、`allowed_source_kinds.items`、`fact_kind`；
- `query-job.schema.json`：`state`；
- `query-result.schema.json`：`metadata.accuracy`、`metadata.coverage.state`；
- `saved-analysis-template.schema.json`：`kind`。

## 范围与语义决策

1. **纯文档、零行为变化**：只增加 9 条 `description`，不增删枚举值、不改任何校验/计划器/存储/SDK/控制台代码。每条表述都有当前实现的代码依据。
2. **逐枚举的代码依据**：
   - `navigation_type`：浏览器 SDK `client.ts` 的 `newPage` 以同一闭集校验，未知值抛 `INVALID_NAVIGATION_TYPE`；`initial` 用于新标签页构造、`reset()` 与空闲换会话后的首个页面，`push` 是省略时的默认值，`replace`/`pop` 对应历史条目替换与进退遍历，`restore` 对应 bfcache 恢复。
   - 事件计划 `status`：`delivery/manifest.go` 只接受 `published` 作为生效发布清单，`draft_example_not_published` 仅出现在编写示例与夹具（`learning-site-event-plan.json`），不得被当作生效发布。
   - 事件计划 `environment`：发布按 project + environment 不可变，机器凭据按单一环境授权，事件只有在该环境已发布时才被接受。
   - `allowed_source_kinds`：`catalog/validation.go` 限定 browser/server、至多两个且去重、发布必填；`server_confirmed` 事实不允许 browser 来源。
   - `fact_kind`：同文件闭集校验、发布必填、与 browser 来源的互斥规则；控制台产品标签为「用户意图 / 客户端观察 / 服务端确认」，描述只复述这一既有信任分级，不发明新语义。
   - 作业 `state`：PG 存储的真实状态机——queued 受排队截止时间约束、running 持有执行租约、succeeded 的结果在 result TTL 内可读；`expireQuery` 有三个触发条件（作业 `expires_at`、排队截止、成功后的结果 TTL）且过期即清空结果；cancel 只能从 queued/running 进入；failed/canceled/expired 都是不可重试终态。
   - 结果 `accuracy`：planner 对非指标引擎默认 `not_applicable`；`approximate` 仅在请求显式指定且算子为 unique 或 p50/p90/p95/p99 时出现（域校验闭集），其余为 `exact`。
   - 覆盖 `coverage.state`：当前引擎在 `clickhouse.go` 恒产出 `provisional`，并随附 `pipeline_completeness_not_established`、`identity_resolution_not_applied` 两条已知缺口；全仓搜索确认 `settled`/`partial` 没有生产产出点，描述显式说明二者是预留给未来结算流程的保留值，避免接入方误读为当前可能返回。
   - 保存分析模板 `kind`：`analysis/template.go` 的 `validatedRequest` 以六闭集（trend/funnel/retention/segment/path/attribution）分派，`events`/`journey` 落入 default 被 `UNSUPPORTED_QUERY_KIND` 拒绝；事件版本经 `event_versions` 或类型化步骤不可变钉版，环境/时间范围/时区只在 Bind 时供给。
3. **语言一致**：`query-result.schema.json` 既有两条中文描述，新增两条沿用中文；其余四个文件无既有描述语言，沿用查询契约切片的英文。
4. **枚举值字节级不变**：五个文件相对 HEAD 的枚举数组逐一比对完全一致，键集合仅多 `description`。
5. **边界**：HTTP 方法等 OpenAPI 路径层常量、看板参数独立白名单不在本切片；不新增任何运行时能力。

## 改动清单

| 文件 | 说明 |
| --- | --- |
| `contracts/event/event-envelope.schema.json` | `navigation_type` 补描述 |
| `contracts/event/event-plan.schema.json` | 4 个枚举补描述 |
| `contracts/query/query-job.schema.json` | 作业 `state` 补描述 |
| `contracts/query/query-result.schema.json` | `accuracy`/`coverage.state` 补中文描述 |
| `contracts/reporting/saved-analysis-template.schema.json` | 模板 `kind` 补描述 |
| `backend/internal/gen/wire/contracts.gen.json` | 重新生成的离线校验 bundle（仅 9 个描述字符串叶） |
| `packages/api-client/src/schema.gen.ts` | 重新生成的 TS 类型（仅 JSDoc，联合类型不变） |

## 验证

### 等价性（无行为面，不做变异）

- 五个源 schema JSON 合法；与 HEAD 结构化对比，9 个枚举的值数组字节一致、仅新增 `description` 键，五个文件裸枚举数全部降为 0。
- Go bundle 与 HEAD 的递归结构对比（含数组逐元素）：恰好 9 处 `added ...description`，无任何值、枚举、数组长度或结构变化。
- `git diff packages/api-client/src/schema.gen.ts` 全部变更为 JSDoc 增行，非注释增删行数为 0，枚举联合类型零变化。
- 本切片没有运行时代码面，弱变异只会产生误导性红，故沿用前三个自描述切片口径，以「枚举零变化 + 全量门禁」证明等价。

### 门禁

- `check_naming.py`、`verify_contracts.py`（含 64 保存分析负例、指标/公式契约）、`tsc --noEmit`、prettier、client 单测 49/49 均通过。
- 全量 `make check` EXIT=0、零 FAIL：Go 32 包 ok、client 49/49、sdk 63/63、console 18/18、生产构建通过。日志：`/tmp/evia-check/cross-contract-enum-docs/make-check.log`。

## 明确未做（边界）

- 不改任何枚举值、校验规则、状态机或运行时行为；不新增 API、字段或能力。
- 不补 HTTP 方法、看板参数白名单等其他闭集的描述（独立资源/路径层，另案处理）。
- 漏斗「严格有序」模式、身份关联图、来源系统字段分析化、热力图/会话回放与外部基础设施（多机房 TLS、parquet、对象存储、SMTP）仍是需单独拍板或外部环境的事项，本切片不自行决定。
