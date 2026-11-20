# 百万查询容量修复验证记录（2026-11-20）

## 交付

按[查询容量规划](../plans/query-capacity-plan.md)第一优先方案交付专用热键列生产化闭环，[复现基线](query-capacity-repro-2026-11-20.md)的 202 次失败在同数据集上清零：

1. **运维命令 `evia property-slots`**（`pipeline.PropertySlotExecutor`）：应用 `Catalog.MigrationSQL()`（槽位列 + winner 元组视图重建，幂等、纯增量）后按事件身份回填——从 `property_tokens` 逐列取词法原样 token 写入槽位（`if(mapContains(...))` 缺失即空），行标记写目录修订号；`mutations_sync=1` 同步等待并回报各行数。真实 CH 集成测试覆盖：应用、回填（含无 token 行保持空槽位走文档路径）、视图投影、幂等重放。
2. **读侧两级剪枝**（`analysis`）：
   - 全命中剪枝：请求的全部属性字段命中目录时，winner 元组剪掉 `property_tokens`（Map 列，内存与读取的主导成本），字段表达式塌缩为「槽位 → 权威文档」两层；未命中任一字段则保持既有三层表达式与列集，行为逐字节不变。
   - 严格模式（`EVIA_PROPERTY_SLOTS_STRICT=true`，运维回填完成断言）：全命中请求连 `properties_json` 文档回退也剪掉，miss 映射为空对象 `{}`（一切 JSON 取值按缺失读 NULL，不会产出非法 JSON）；经 `CompileWithCatalogStrict` 显式传参，默认关闭时 `CompileWithCatalog` 行为不变。
3. **配置**：`EVIA_PROPERTY_SLOTS_STRICT` 经 `config` 校验注入查询引擎；`property-slots` 命令进入命令与 ClickHouse 配置白名单。

## 同数据集 A/B 矩阵（唯一变量为读取路径）

数据与环境与复现基线完全一致（v3 项目、100 万唯一 + 10 万重放、同预算、同样本量），三轮对照：

| 轮次 | 读取路径 | 结果 |
| --- | --- | --- |
| 基线（复现记录） | 文档 + token Map | 526/728，属性类 182 全挂、并发类 20 |
| 槽位（未剪 Map） | 槽位命中但元组仍带 Map | 528/728，属性类仍全挂（Map 列是死重，Code 307 读超 270MB/87.5 万行） |
| 剪 Map + 严格 | 槽位命中、元组剪 Map 与文档 | **728/728，零失败** |

第三轮逐组：count/exact_uid/hour_bucket/section_group/high_cardinality_uid_filter/property_filter/journey/ordered_funnel 在并发 1/4/8 全部 30/30 通过；工具内置语义断言（分组分布、筛选计数、精确可见性 1,000,000/100,000）全部通过——等价性由真实结果值证明，非仅「跑通」。并发类 20 次失败随剪枝一并清零（单查询内存与读取量下降）。

## 本地门禁

`make backend-test` 全量 EXIT=0；analysis 与 pipeline 的目标集成测试（槽位执行器、身份扫描器、CH 等价性套件）`-race` 通过；`dedicated_reader_test` 契约更新为「全命中剪 Map、保留文档回退」。pipeline 套件中两个前存在失败与本批无关（clean tree 复验同样失败：readiness 收据分组依赖本机未运行的探针写入；HA 用例缺 `EVIA_TEST_CH_HA_*` 环境）。

## 边界（如实声明）

- 严格模式是运维断言：未回填的行在全命中请求下会读出 NULL 而非文档值。操作顺序必须固定为「property-slots 应用+回填完成 → 再开启 STRICT」；命令回报的 materialized_rows 是断言依据。
- 热键目录需人工评审（本批 benchmark 目录 6 槽位为演示性选择，生产目录应来自真实分布审计）；目录外属性字段仍走文档路径（行为不变，但同类预算问题在目录外属性上依旧存在）。
- 728/728 是合成 Zipf 分布下的单节点证据；多副本、持续混合读写、长保留与大合并窗口下的表现未验证。
- 原始工件：`/tmp/million-queries-{v3,slots,pruned,strict}.json`、`/tmp/evia-property-catalog.json`（本机，不进仓库）。
