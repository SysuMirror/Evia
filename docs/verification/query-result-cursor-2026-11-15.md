# 查询结果快照续页游标验证记录（2026-11-15）

## 背景与缺口

分析任务完成后，结果以不可变快照存入 PostgreSQL `analysis_query_jobs.result_json`
（上限 500 行 / 1.5 MiB）。读取端点 `GET /api/v1/projects/{project_id}/queries/{query_id}/result`
早已在 OpenAPI 预留 `cursor` 查询参数、在 `analysis.Result` 模型预留 `next_cursor` 字段，
但传输层对任何非空 `cursor` 一律以 `UNSUPPORTED_QUERY_CURSOR` 拒绝，能力矩阵标注
「结果游标未实现」。

需要区分两类游标，本次只放开后者：

| 游标 | 位置 | 语义 | 本次处理 |
| --- | --- | --- | --- |
| 请求体游标 | `QueryRequest.cursor`（`analysis/validation.go`） | 引擎级时间线续拉，需改 ClickHouse 取数 | 仍显式拒绝，保持不变 |
| 结果快照游标 | 结果读取端点 `?cursor/&limit=` | 对已物化快照分页读取 | 本次实现 |

## 设计

- 复用既有 `internal/platform/cursor` HMAC 编解码器（24 小时过期、scope 绑定），
  不新增表、不新增迁移、不改动引擎与存储。
- 游标内嵌 JSON 位置 `{o: 起始偏移, l: 页大小}`，scope 为
  `analysis_query_result:{actor}:{project}:{query_id}`，与单一查询结果快照绑定，
  无法跨查询重放。快照不可变，偏移稳定不漂移。
- `limit` 为 1–200 的严格整数；省略 `cursor` 与 `limit` 时直接返回完整快照，
  `next_cursor` 为 null，默认响应逐字节不变。
- 分页参数在读取快照**之前**完成签名、scope、偏移与页大小一致性校验，任何非法输入
  都以 400 失败关闭且不触达结果存储。
- 首页之后若同时携带 `limit`，必须与首页页大小一致，否则 `INVALID_LIMIT`，
  杜绝跨页边界导致的跳过或重复。
- 终端游标回放（偏移等于总行数）幂等返回空行且 `next_cursor` 为 null；
  偏移越界、页大小为 0、位置 JSON 含未知字段一律 `INVALID_CURSOR`。
- 每页仍返回 `columns` 与 `metadata`，仅对 `rows` 切片，响应自描述。

## 改动面

- 契约：`contracts/http/openapi.json` 为 `getQueryResult` 新增 `limit`（1–200，无默认值），
  并把 `cursor` 长度收敛到 2048 以对齐编解码器硬限制；重新生成 `schema.gen.ts`。
- 后端：`backend/internal/transport/control/analysis.go` 新增
  `resolveResultPosition` / `sliceQueryResult` / `parseQueryResultLimit`，
  `scope` 仅在结果读取路由放行 `cursor`、`limit` 两个参数，其余查询路由仍拒绝一切查询串。
- 客户端：`packages/api-client` 的 `getQueryResult` 改为 options 入参，支持 `cursor`/`limit`，
  既有调用点迁移为 `{ signal }`。
- 控制台：分析工作区对原始事件（`events`）与行为链（`journey`）首页按 100 行有界拉取，
  结果表在存在 `next_cursor` 时提供「加载下一页」，按 queryID 键控累积、切换查询不串页；
  聚合类查询仍读取完整快照，图表数据不受影响。

## 先红（变异取证）

在 `analysis_test.go`（已跟踪文件，规避沙箱新文件读损坏）追加
`TestAnalysisQueryResultPagination`，含 7 个子测：legacy 全量快照兼容、逐页遍历重建、
limit 边界、伪造/越权游标、页大小一致性、终端回放与越界、路由参数隔离。

变异 A——强制永不签发 `next_cursor`（`if false && end < total`）：

```
--- FAIL: TestAnalysisQueryResultPagination/walk_every_page_and_reconstruct_the_snapshot
    analysis_test.go:175: pages=1 rows=2
```

变异 B——去掉首页页大小一致性校验（`if false && requested != position.Limit`）：

```
--- FAIL: TestAnalysisQueryResultPagination/page_size_must_stay_consistent_with_the_first_page
    analysis_test.go:239: status 200, want 400
```

两次变异均如期变红；还原后 `sha256sum -c` 与变异前摘要完全一致，测试转绿。

测试过程中还抓到一处测试侧弱点：翻转签名段**最后一个** base64 字符可能只落在末尾
2 个无效高位，解码出的 HMAC 字节不变导致验签仍通过；改为翻转签名段**首字符**
（必然改变 HMAC 首字节）后，伪造用例如期被拒。

## 验证命令与结果

```
go test ./internal/transport/control/ -run TestAnalysisQueryResultPagination -v -count=1
# 7 个子测全部 PASS
pnpm test:client
# 35 项全部 pass（含新增 query result continuation 编码用例）
pnpm typecheck
# tsc --noEmit 零错误
```

完整门禁 `make check PYTHON=<venv>/python` 退出码 0：32 个 Go 包 `go test -race` 全部 ok、
零 FAIL；prettier 全量格式检查通过；api-client 35 项、browser-sdk 55 项测试全过；
控制台构建成功。

## 边界与不变量

- 默认无参路径逐字节不变，既有控制台、机器客户端与全部既有测试零影响。
- 请求体引擎级续拉仍 fail-closed，本切片不改变 ClickHouse 取数语义与 500 行/1.5 MiB 快照上限。
- 游标签名密钥复用 `EVIA_CURSOR_KEY`，与项目列表、治理、任务列表等既有分页同一套 HMAC 设施。
