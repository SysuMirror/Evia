# Parquet 导出依赖评估与设计备忘录

> 状态：依赖评估已完成（2026-11-19，真实代理探针），语义方案待拍板后进入实现切片。
> 对应覆盖清单 `feature-coverage.md` 数据导出行的剩余项「parquet（需评估依赖）与对象存储适配」。
> 对象存储适配仍属厂商环境阻塞（见 [production-readiness](production-readiness.md) 第 5 节），本备忘录只覆盖 parquet 格式本身。

## 1. 现状与接入面（已核实）

导出 v1 已交付 json/csv/ndjson 三格式，parquet 切片的全部接入点与既有格式同构：

| 层 | 接入点 | 既有形态 |
| --- | --- | --- |
| 领域常量 | [exporting.go](../backend/internal/exporting/exporting.go) | `FormatJSON/FormatCSV/FormatNDJSON` + 词汇守卫 #5 |
| 格式校验 | [service.go](../backend/internal/exporting/service.go) `NormalizeFormat` | 闭集 switch，未知值 fail-closed |
| 渲染分发 | [artifact.go](../backend/internal/exporting/artifact.go) `FileArtifactStore.Write` | switch 到 `renderCSV/renderNDJSON`，默认 JSON |
| 媒体类型 | [export.go](../backend/internal/transport/control/export.go) `artifactMediaType` | MIME + 扩展名映射 |
| 存储约束 | 迁移 019/021/039 | CHECK 三次加宽，下一迁移编号 **042**，照 039 模式 drop+add |
| 契约 | `ExportInput.format`、`ExportJob.format`、`SavedAnalysisExportInput.format` 三处 enum | codegen 三步同步 |
| 控制台/客户端 | `apps/console/src/exports/exports.tsx`、`@evia/api-client` | 格式选择器与预算校验 |
| 行数/字节护栏 | `MaxResultRows=10000`、`MaxArtifactBytes=32MiB` | 渲染后统一拦截，parquet 自动复用 |

## 2. 依赖评估（真实探针证据，2026-11-19）

后端当前仅 6 个直接依赖，刻意保持精简；在 `/tmp/parquet-eval` 临时模块（不污染仓库）实测：

- 代理可用：`GOPROXY=https://goproxy.cn,direct` 下 `go list -m -versions` 正常，候选库 `github.com/parquet-go/parquet-go` 最新 **v0.32.1**；
- 纯 Go、无 cgo；go.mod 要求 `go 1.26.0`，本仓工具链 1.27.3 满足；
- 许可证：主库 **Apache-2.0**；9 个间接依赖全部为宽松许可证（Apache-2.0/BSD-2/BSD-3/MIT，含 google/uuid、klauspost/compress、pierrec/lz4、andybalholm/brotli、parquet-go/bitpack、jsonlite、x/sys、protobuf、go-geom），与自托管发行兼容；
- 最小写入程序实测成功（三行结构体 → 689 字节合法 parquet）；实际链接集为 parquet-go 子模块 + uuid + brotli/lz4/snappy 压缩器；`go-geom`/`protobuf` 仅测试图引用，不进二进制但会留 go.sum 记录；
- 替代方案：`apache/arrow/go` 传递依赖与体积显著更重；手写 parquet（Thrift compact 元数据 + 多种编码）不具生产可行性，排除。

**评估结论**：依赖本身可行且许可证干净，代价是 go.mod 新增 1 个直接依赖 + 9 个间接依赖、二进制内置三种压缩 codec。这是本仓迄今为止最大的一次依赖扩容，需在实现切片前明确接受。

## 3. 语义分叉：列类型映射（需拍板的开放选择）

Parquet 是强类型列存，与 csv/ndjson 的「一切皆文本、json.Number 词法无损」路线存在真实取舍。结果列类型词汇实测为 7 类：`string/number/integer/boolean/datetime/array/object`（另有个别夹具标签）。

### 方案 A：全列字符串（词法无损优先）

每列均为 `BYTE_ARRAY(UTF8)`，单元格复用 csvCell 的词法渲染（`1e2`、`1.00`、`-0.0`、超 int64 整数逐字保留）。

- 优点：与既有词法无损硬约束完全一致，实现最简单，无不可映射值；
- 缺点：parquet 消费者（Spark/Arrow/DuckDB/BI）拿到的全是字符串，数值谓词下推、列式统计与压缩优势基本失效，本质是「parquet 容器装 CSV」，行业实践中属反模式。

### 方案 B：按 Column.Type 强类型映射 + 不可映射 fail-closed（推荐）

| Column.Type | parquet 逻辑/物理类型 | 规则 |
| --- | --- | --- |
| boolean | BOOLEAN | 直接映射 |
| integer | INT64 | json.Number 解析 int64；越界（含 uint64 超界）fail-closed `EXPORT_PARQUET_UNSUPPORTED` |
| number | DOUBLE | 浮点归一化（`1e2`/`1.00` 不再保留词形，`-0.0` 作为浮点负零保留） |
| string | BYTE_ARRAY(UTF8) | 直接映射 |
| datetime | BYTE_ARRAY(UTF8) | 保留 RFC3339 词形（v1 不做时间戳物理类型，避免时区二次解释） |
| array | LIST 组 | 元素按同型规则；混合元素降级 JSON 字符串 |
| object/json | BYTE_ARRAY(UTF8) | 紧凑 JSON 字符串 |
| 未知/同列混型 | — | fail-closed，与 `EXPORT_CSV_UNSUPPORTED` 同哲学 |

- 优点：交付 parquet 的真实价值（类型化列、谓词下推、列式压缩），符合数据湖消费者预期；
- 缺点：number 列不保证 token 词法无损（但 json/csv/ndjson 三格式仍在，词法无损需求有既有出口）；integer 越界是显式失败而非静默截断；
- 护栏：归一化规则逐类型单测钉死，fail-closed 负例覆盖越界整数、混型列、未知类型。

### 方案 C：混合（部分强类型 + number 保留字符串）

boolean/integer/string 强类型，number/datetime 保留词法字符串。列内规则不一致、消费者心智负担高，不推荐。

**建议**：采用方案 B。理由：parquet 是新增格式而非替代，词法无损场景仍由三既有格式覆盖；fail-closed 哲学与本仓既有导出错误码一致；类型映射规则可完整测试。方案 A 仅在「明确不允许任何数值归一化」的产品决策下才选。

## 4. 实现切片计划（拍板后执行，沿用后端→真实验证→前端顺序）

1. `go get github.com/parquet-go/parquet-go@v0.32.1`，记录间接依赖与许可证；
2. 契约三 schema 的 format enum 加 `parquet`，codegen 同步 Go wire、bundle 与 TS；
3. 迁移 042 照 039 模式把 CHECK 加宽为四值；
4. `exporting` 新增 `parquet.go`：`FormatParquet` 常量、`renderParquet(result)`（方案 B 映射表 + fail-closed）、`NormalizeFormat`/`artifactMediaType`（`application/vnd.apache.parquet`、`.parquet`）与 `Write` switch 接线；
5. 守卫 #5 的格式枚举断言自动覆盖新值；补词法/越界/混型单测与 PG 集成（worker 驱动完成、下载 sha256 校验）；
6. 控制台格式选择器与 api-client 加 parquet；Playwright 补一条端到端；
7. 更新 feature-coverage L36、production-readiness 与导出模块文档，分层原子提交。

## 5. 不在本切片范围

- 多段上传、SSE-KMS、跨区域对象存储：需真实厂商凭证与网络，维持外部阻塞；
- parquet 压缩级别/行组大小调优：v1 用库默认值，容量门控实验另立；
- 不改既有三格式的任何行为与错误码。
