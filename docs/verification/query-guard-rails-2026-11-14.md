# 查询护栏完整性与取消隔离验证记录（2026-11-14）

## 范围

[多租户内存隔离](tenant-query-isolation-2026-11-14.md)覆盖了内存（Code
241）与读取行数（Code 158）两个护栏。本切片收口分析查询设置闭集
`backend/internal/analysis/clickhouse.go` 中其余尚未有直接证据的护栏，
并验证 Go 引擎在调用方 context 取消时发出的 `KILL QUERY ASYNC` 路径：

- `readonly=2` 写保护；
- `max_execution_time` 执行超时；
- `max_result_rows` / `max_result_bytes` 结果集溢出；
- `KILL QUERY` 后查询真正停止、不继续占用服务器，且取消作用域隔离
  （不波及兄弟查询）。

无生产代码变更。每个查询附加与 Go 分析引擎逐项相同的 SETTINGS（直接
复用上一探针的 `PRODUCTION_QUERY_SETTINGS` 单一事实源），全部 SQL 构造
与异常分类为纯函数并由单测锁定。

## 交付内容

- `tooling/benchmark/query_guard_probe.py`：以异常类型名为主、错误码为
  辅的分类器，五个场景的 SQL 构造与 KILL 并发底座。
- `tooling/benchmark/test_query_guard_probe.py`：24 项纯逻辑单测
  （benchmark 门控累计 168 项）。

## 实测异常码表（ClickHouse 26.8，先实证后编码，未臆测）

| 护栏 | 异常类型名 | Code | 备注 |
| --- | --- | --- | --- |
| readonly 写保护 | READONLY | 164 | CREATE TABLE 被拒 |
| 执行超时 | TIMEOUT_EXCEEDED | 159 | |
| 结果行数/字节溢出 | TOO_MANY_ROWS_OR_BYTES | 396 | **两行护栏共用此码** |
| 读取字节超限 | TOO_MANY_BYTES | 307 | |
| 读取行数超限 | TOO_MANY_ROWS | 158 | |
| 内存超限 | MEMORY_LIMIT_EXCEEDED | 241 | 上一探针 |
| 查询被取消 | QUERY_WAS_CANCELLED | 394 | KILL QUERY |

**关键设计点**：Code 396 同时表示结果行数与字节超限，Code 159 在不同
语境也会复用，因此分类器以异常消息括号内的类型名（如
`(TOO_MANY_ROWS_OR_BYTES)`）为主、Code 仅作佐证，并对 396 再按消息中
`max rows` / `max bytes` 文本细分。单测显式覆盖了「同码不同标签」与
「类型名优先于复用码」两个歧义用例。

## 五场景结果（两轮独立运行，标签与码逐值一致）

| 场景 | 观测标签 | Code | 时延 |
| --- | --- | --- | --- |
| readonly-write | readonly_write_rejected | 164 | 108 ms |
| execution-timeout | execution_timeout | 159 | 1113 ms |
| result-rows-overflow | result_rows_limit_exceeded | 396 | 122 ms |
| result-bytes-overflow | result_bytes_limit_exceeded | 396 | 133 ms |
| kill-query-cancellation | query_cancelled | 394 | — |

KILL 场景断言全部成立：长查询在 KILL 前出现在 `system.processes`、
KILL 后从该表消失、客户端收到 394，随后执行的兄弟查询成功（label=ok），
证明取消是作用域隔离的，不会误伤其他查询。

## 先红后绿（三个真实护栏触发陷阱）

1. **readonly 误用 CREATE DATABASE**：DDL 不接受 `readonly` 作为
   SETTINGS 键，返回 Code 62 语法错误而非 164。改为在预置 scratch 库内
   `CREATE TABLE ... SETTINGS readonly=2`，才确定性命中 READONLY（164）。
2. **超时被读取护栏抢先**：用大 `numbers()` 测超时时，查询在 1 秒内先
   撞 `max_rows_to_read=2000000`（158），永远到不了 30s 超时。改用
   `SELECT sleep(2)` 并仅覆盖 `max_execution_time=1`（其余生产护栏保留），
   在读取上限内确定性触发 159。
3. **结果字节护栏不拦流式 SELECT**：直接 `SELECT repeat(...)` 走流式输出
   不累计 `max_result_bytes`，实测放行；改用 `groupArray` 把字符串物化
   成单列结果后才触发 396（max bytes 4MiB vs current 10.3MiB）。

## 边界与诚实声明

- 超时场景把 `max_execution_time` 从生产值 30s 缩到 1s 以便可测，验证的
  是 `timeout_overflow_mode=throw` 的拒绝机制而非生产超时时长；该覆盖
  仅限这一个键，其余 SETTINGS 与生产逐项一致。
- 探针不建业务表（护栏即时触发），仅 readonly 场景需要一个 scratch 库，
  结束 SYNC 删除，两轮后零 `evia_guard_*` 残留。
- KILL 验证的是引擎级取消语义与作用域隔离；Go `ClickHouse.kill()` 的
  HTTP 调用接线由其单元测试覆盖，本探针证明的是被调用的 KILL 语句在
  服务器侧确实终止查询并释放进程槽位。
