# 导出 NDJSON 流式格式验证记录（2026-11-18）

## 问题（确定性缺口）

导出产物格式闭集只有 `json`（完整 `analysis.Result` 文档）与 `csv`（表格行）。面向数据管道的流式摄取缺少标准的「每行一条记录」格式：JSON 整文档必须整体解析、无法逐行消费，CSV 又丢类型。本切片新增 **NDJSON**（newline-delimited JSON / JSON Lines），不引入新依赖、不改变查询语义。

## 范围与语义决策

1. **标准 NDJSON 语义，无开放选择**：每个结果行 `result.Rows[i]` 序列化为一个紧凑 JSON 对象并以换行 `\n` 结束；无外层信封、无 metadata/columns 块。这是 NDJSON/JSONL 的通用约定，每行自描述、可被 jq/Spark/BigQuery 等逐行读取。
2. **空结果即空流**：零行产生零字节产物，这是合法的空 NDJSON 流，下游管道无需特判。
3. **与 JSON 导出共享编码语义**：渲染用 `json.Marshal`（非自定义 Encoder），因此 `json.Number` 保留词法形态（整数、高精度小数、`-0.0`/`1e2` 不被重新解析），HTML 转义行为与完整 JSON 导出一致；nil 单元格输出 `null` 而非空串。
4. **沿用 021 的迁移扩词模式**：迁移 `039` 仅把 `export_jobs_format_check` 从 `('json','csv')` 放宽到 `('json','csv','ndjson')`，默认值仍为 `json`，旧行继续合法；迁移通过 `//go:embed migrations/*.sql` 自动加载，无硬编码列表。
5. **fail-closed 闭集不削弱**：`NormalizeFormat` 与数据库 CHECK、OpenAPI 枚举三处一致收口；未知格式（如 `parquet`）仍在领域层与存储层双重拒绝（既有负例保持有效）。
6. **媒体类型与扩展名**：下载返回 `application/x-ndjson; charset=utf-8` 与 `.ndjson` 文件名，未知格式仍回退 JSON 以保证授权下载不失败（与既有回退约定一致）。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 迁移 | `migrations/039_export_format_ndjson.sql` | 放宽 export_jobs 格式 CHECK，加入 ndjson |
| 领域 | `exporting/exporting.go`、`service.go`、`artifact.go`、`ndjson.go` | 新增 `FormatNDJSON` 常量、`NormalizeFormat` 闭集、逐行渲染器与 Write 分发 |
| HTTP | `transport/control/export.go` | `artifactMediaType` 增加 ndjson 媒体类型/扩展名（switch 重构，json/csv 分支不变） |
| 契约 | `contracts/http/openapi.json` | 两处 format 枚举加 ndjson；重跑 codegen（Go bundle、TS 两处联合类型） |
| 控制台 | `exports/model.ts`、`exports/exports.tsx` | `ExportFormat` 联合类型与下拉选项 |
| 测试 | `ndjson_test.go`、`csv_test.go`、`transport/control/export_test.go`、`postgres/export_integration_test.go`、`tests/browser/export-model.spec.ts` | 渲染/闭集/落盘/HTTP 媒体类型/真实 PG CHECK/控制台模型 |
| 文档 | `docs/feature-coverage.md`、`docs/runtime.md` | 能力核销与 worker 产物格式清单 |

## 验证

### 领域单测（`go test ./internal/exporting`）

- `TestRenderNDJSONOneCompactRecordPerLine`：每行可独立反序列化、单行无内嵌换行、逐行换行结尾；`json.Number` 的 `42`/`1e2` 词法保真，数组单元格仍是 JSON、nil 为 null。
- `TestRenderNDJSONEmptyResultIsEmptyStream`：零行 → 零字节。
- `TestNormalizeFormatAcceptsNDJSONAndRejectsUnknown`：空值默认 json，json/csv/ndjson 归一，`parquet` 拒绝；既有 `TestNormalizeFormatVocabulary` 同步纳入 ndjson。
- `TestFileArtifactStoreWritesFormatSpecificFiles`：扩展断言 ndjson 产物用 `.ndjson` 扩展名且恰好每一行一条记录。

### HTTP 单测（`go test ./internal/transport/control`）

- 新增 `TestExportDownloadUsesNDJSONMediaType`：断言 `Content-Type=application/x-ndjson; charset=utf-8`、`Content-Disposition` 含 `.ndjson`、流式字节与产物逐字一致，且保留 no-store 等共享边界头。

### 真实 PostgreSQL 集成（`-tags=integration`，迁移序列实际应用）

- 新增 `TestExportNDJSONFormatPersistedAndReplayed`：ndjson 作业经迁移 039 的 CHECK 成功持久化、幂等重放保留格式、worker claim 不丢格式；既有 `parquet` 负例仍被存储边界拒绝。
- 整个 `internal/storage/postgres` 集成包通过，耗时 98.8s，EXIT=0，零 FAIL，证明 039 在完整迁移序列中干净应用且无回归。

### 控制台

- Playwright 模型套件 107 项通过；`export-model.spec.ts` 增加 ndjson 透传用例；`tsc --noEmit` 通过（类型联合收口）；prettier 全过。临时 model 配置用完即删。

### 变异红绿（sha256 逐字节还原，禁用 git checkout，基线 `/tmp/evia-check/ndjson/SHA256SUMS`）

1. 移除 `NormalizeFormat` 的 ndjson 分支 → `TestNormalizeFormatVocabulary` 与 `TestNormalizeFormatAcceptsNDJSONAndRejectsUnknown` 同时变红（ndjson 被误拒）；还原后三文件哈希 OK、复绿。
2. 移除 `artifact.go` Write 的 ndjson 分发 → 产物退化为 60 行带信封的完整 JSON 文档，`TestFileArtifactStoreWritesFormatSpecificFiles` 的「每行一条记录」断言精确变红；还原后哈希 OK、复绿。

### 关于前端变异的说明

控制台本切片是声明式类型联合与静态下拉选项，`validateExportForm` 对 format 为纯透传、无运行时分支，因此不存在能被模型测试捕获的诚实运行时变异点；其正确性由 `tsc` 构建门禁（类型联合）与后端 `NormalizeFormat`/DB CHECK 的变异测试共同保证，未制造弱变异来凑数。

## 明确未做（边界）

- 不实现 parquet（需评估新依赖与列式编码，feature-coverage 仍列为待评估）；不做对象存储适配。
- NDJSON 只承载结果行，不重复 JSON 整文档的 metadata/columns；需要完整信封的调用方继续用 `json` 格式。
- 不改 1 万行 / 32MiB 产物上限、一次性下载授权 TTL、租约与五态机等既有生命周期护栏。
