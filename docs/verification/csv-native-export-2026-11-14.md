# CSV 导出生成原生评估类型缺陷修复验证记录（2026-11-14）

## 背景与缺陷

CSV 格式此前已全链路交付（迁移 021、词法无损渲染、`text/csv` 下载、控制台可选），
但 `backend/internal/exporting/csv.go` 的 `csvCell()` 只覆盖
`json.Number` / `bool` / `string` / `[]any` / `map[string]any`。这套类型面向
ClickHouse 经 JSON 解码后的结果。

进程内 Go 评估器（funnel、retention、segment、path、attribution，位于
`backend/internal/analysis/*.go`）不经过 JSON 往返，直接把原生 Go 类型写入
`analysis.Result.Rows`。导出 worker 调用链为 `Engine.Execute` →
`Artifacts.Write` → `renderCSV`，中间同样没有 JSON 往返。因此对这些分析模型
发起 CSV 导出时，原生单元格落入 `default` 分支，整体以
`EXPORT_CSV_UNSUPPORTED` 失败关闭；JSON 导出走 `json.Marshal` 不受影响。

评估器实际产出的原生类型（逐一核对源码）：

| 模型 | 字段 | 原生类型 |
| --- | --- | --- |
| path | `path` | `[]string` |
| path / segment / attribution / funnel | `subjects`、`step_index`、`count` | `int` |
| segment / attribution | `share`、`credit` | `float64` |
| funnel | `conversion_rate` | `float64`（或 `nil`） |
| retention | `retained` | `[]int` |
| retention | `retention_rate` | `[]any`（既有分支已覆盖） |

## 先红（复现真实缺陷）

在既有 `csv_test.go` 中加入与评估器同构的原生类型结果（`int`、`float64`、
`[]string`、`[]int`，以及 `int64`/`uint64`/`float32`/`[]int64`/`[]float64`/
`[]bool` 同族），修复前运行：

```
--- FAIL: TestRenderCSVNativeEvaluatorTypes
    csv_test.go: CSV export must support native evaluator types:
    EXPORT_CSV_UNSUPPORTED: 结果包含无法序列化为 CSV 的单元格类型
--- FAIL: TestRenderCSVNativeScalarAndSliceFamily
    csv_test.go: native scalar/slice family must render: EXPORT_CSV_UNSUPPORTED ...
```

精确复现了 worker 对进程内评估结果做 CSV 导出必然失败的缺陷。

## 修复

`csvCell()` 在 `bool` 分支后补齐原生数值宽度（`int`/`int8`/`int16`/`int32`/
`int64`、`uint`/`uint8`/`uint16`/`uint32`/`uint64`、`float32`/`float64`），
整数用 `strconv.FormatInt/FormatUint`、浮点用 `strconv.FormatFloat(..., 'g', -1, ...)`
最短往返表示；结构化分支在既有 `[]any`/`map[string]any` 基础上并入具体切片
`[]string`/`[]int`/`[]int64`/`[]float64`/`[]bool`，统一 `json.Marshal` 成紧凑
JSON，再由 `csv.Writer` 按 RFC 4180 加引号。

不变量：

- `json.Number` 分支保持在原生数值分支之前，ClickHouse 词法（如 `1e2`、
  `1.00`、`-0.0`）仍逐字节保留，不被重序列化；
- `default` 仍对无法识别的类型（如 `chan`）失败关闭，不静默吞掉；
- 仅扩展序列化分支，未改动列顺序、写入与产物元数据（sha256/字节/行数）逻辑。

## 后绿（验证证据）

新增三条用例，全部落在既有 `csv_test.go`：

- `TestRenderCSVNativeEvaluatorTypes`：断言精确行
  `2,0.6666666667,1,"[""open"",""view""]","[1,0,1]"`；
- `TestRenderCSVNativeScalarAndSliceFamily`：锁定整数宽度、无符号、`float32`
  及三类具体切片的紧凑 JSON；
- `TestFileArtifactStoreWritesNativeEvaluatorCSV`：经与导出 worker 相同的
  `FileArtifactStore.Write` 路径物化 `.csv`，校验扩展名、文件后缀字节与
  产物元数据，覆盖真实失败面而非仅渲染函数。

既有用例 `TestRenderCSVPreservesColumnOrderAndLexicalNumbers`（词法无损、
RFC 4180 引号、nil 空单元格）与 `TestRenderCSVRejectsEmptyColumnsAndUnknownTypes`
（无列结果与 `chan` 等未知类型仍拒绝）保持通过，证明默认路径行为不变。

本缺陷为纯 Go 序列化，不依赖外部容器；`go test ./internal/exporting
./internal/analysis` 与整仓 `go build ./...`、`go vet`、`gofmt` 均通过，
完整 `make check` 退出码 0。

## 边界

- 计算得到的 `float64` 采用最短往返文本，属于求值结果而非需保词法的原始令牌，
  与 `json.Number` 的词法无损职责互不重叠；
- 未支持的具体类型仍走 `default` 失败关闭；若未来评估器引入新的单元格类型，
  必须在此显式补分支并补测试，不允许隐式放宽。

## 涉及文件

- `backend/internal/exporting/csv.go`：`csvCell()` 原生数值与具体切片分支。
- `backend/internal/exporting/csv_test.go`：先红后绿三条用例。
