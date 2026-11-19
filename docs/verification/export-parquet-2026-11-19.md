# Parquet 导出格式全链路切片验证记录（2026-11-19）

## 问题（确定性缺口）

数据导出 v1 仅支持 json/csv/ndjson，[feature-coverage](../feature-coverage.md) 导出行明确剩余「parquet（需评估依赖）与对象存储适配」。前置依赖评估见 [design-parquet-export](../design-parquet-export.md)：parquet-go v0.32.1 纯 Go、Apache-2.0、9 个间接依赖全部宽松许可证，最小写入实测可行。本切片按备忘录方案 B 落地强类型列存，对象存储仍维持外部环境阻塞。

## 语义决策（已在设计备忘录冻结，本切片照此执行）

采用**按 `Column.Type` 强类型映射 + 不可映射 fail-closed**，不做「parquet 容器装 CSV」的全字符串方案。词法无损需求仍由 json/csv/ndjson 三既有格式承接，parquet 作为新增格式不替代任何现有行为：

| Column.Type | parquet 物理类型 | 规则 |
| --- | --- | --- |
| boolean | BOOLEAN | 直接映射 |
| integer | INT64 | json.Number 必须是整数词形，越界/浮点词形 fail-closed |
| number | DOUBLE | 浮点归一化（`1e2`→100），负零保留 |
| string / datetime | BYTE_ARRAY(UTF8) | datetime 保留 RFC3339 词形；json.Number 保留原词形 |
| array:string/boolean/integer/number | LIST | 元素同型规则；泛型 `array` 按元素推断，混合元素降级整列 JSON 字符串 |
| object/json | BYTE_ARRAY(UTF8) | 紧凑 JSON 字符串 |
| 未知类型/同列混型 | — | `EXPORT_PARQUET_UNSUPPORTED` fail-closed |

## 交付内容

1. 契约：`ExportInput/ExportJob/SavedAnalysisExportInput` 三处 format enum 加 `parquet`，codegen 三步同步（Go wire、离线 bundle、TS schema）；
2. 迁移 [042_export_format_parquet.sql](../../backend/internal/storage/postgres/migrations/042_export_format_parquet.sql)：照 021/039 模式 drop+add CHECK 加宽为四值，零 backfill，默认仍 json；
3. 依赖：go.mod 新增直接依赖 `github.com/parquet-go/parquet-go v0.32.1`（纯 Go 无 cgo，9 间接依赖，许可证 Apache/BSD/MIT）；
4. 渲染器 [parquet.go](../../backend/internal/exporting/parquet.go)：动态 schema（`parquet.Group` + Optional 节点）+ map 行写入，全部列可选以容纳 nil 单元格；`NormalizeFormat`、`FileArtifactStore.Write` 分发、下载媒体类型（`application/vnd.apache.parquet`、`.parquet`）全接线；
5. 复用既有护栏：1 万行/32MiB 上限、根目录逃逸防护、sha256 产物校验、一次性下载授权均不区分格式自动生效；
6. 前端：控制台格式选择器加 Parquet 选项，`ExportFormat` 类型加宽，api-client schema 由 codegen 更新，Playwright model 用例补 parquet 正例。

## 刻意保留（边界）

- 既有三格式行为、错误码与字节输出零变化；
- 未广告的内部类型标签不纳入映射，未知类型 fail-closed 而非猜测；
- parquet 压缩 codec/行组大小用库默认值，容量调优另立探针；
- 多段上传、SSE-KMS、跨区域对象存储仍为厂商环境阻塞，不在本切片。

## 守卫有效性取证（变异红绿）

基线 sha256（parquet.go、contract_alignment_test.go、contracts.gen.json）存 `/tmp/evia-check/parquet/sha256-baseline.txt`，原件备份于 `baseline/`。

- 变异 A（契约漂移）：从生成 bundle 的 `ExportJob.format` enum 删除 parquet → 导出词汇守卫 #5 报 `export job format drift: contract [csv json ndjson] != Go [csv json ndjson parquet]` 变红；
- 变异 B（类型降级）：integer 列节点从 INT64 改为 DOUBLE → 强类型往返测试报 `integer column must be INT64, got float64 42` 变红；
- 双双备份还原，`sha256sum -c` 三文件 OK，复跑全包复绿，零残留。

## 验证

- gofmt 零差异；`internal/exporting` 单测 6 个 parquet 用例（七类型强类型往返、字符串列数字词形保留、四类 fail-closed 负例、混合数组 JSON 降级、空列/零行、NormalizeFormat）全过；
- 全量 `make check` 两轮 EXIT=0、零 FAIL：Go 33 包 ok（`go test -race ./...`）、api-client 51、browser-sdk 63、codegen 幂等（generated-check）、prettier/typecheck/生产构建全过。日志：`/tmp/evia-check/parquet/make-check.log`、`make-check-2.log`；
- Playwright model 套件 `export-model.spec.ts` 16/16 通过（含新 parquet 输入用例，`--config.verify-deps-before-run=false` 绕过本机 pnpm 预检）；
- 真实 PostgreSQL：新增 `TestExportParquetFormatPersistedAndReplayed` 验证 parquet 作业经迁移 042 CHECK 持久化、幂等重放与 claim 不丢格式；既有 CSV 测试的坏格式占位从 parquet 改为仍不支持的 avro，继续证明 CHECK 拒绝未知值；整包 `go test -race -tags integration -skip TestFailoverBaselineWrite ./internal/storage/postgres` → ok 251.0s、零 FAIL。日志：`/tmp/evia-check/parquet/pg-integration.log`。

## 明确未做（边界）

- 不实现 parquet 读取端/查询引擎对接，本切片只保证导出产物可被标准 parquet 工具读回（单测内已用 parquet-go 读回校验物理类型）；
- 不改变契约 numeric 字段形状与任何既有格式输出；
- 对象存储适配等待真实厂商凭证与网络环境。
