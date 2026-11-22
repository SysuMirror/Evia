# 会话回放容量基准

本切片定义会话回放容量报告的**纯核对与通过门禁**。`tooling/benchmark/session_replay_capacity.py` 不采集事件、不调用 Evia、不启停 worker，也不创建数据库记录；它只读取一份私有汇总 JSON，并以固定策略生成一份私有评估 JSON。因此，单元测试通过只证明报告政策，不能当作浏览器、数据库、worker 或生产容量证据。

本文没有实测结果。只有在专用本地合成环境完成全部矩阵、保留原始观测并由评估器返回 `passed: true` 后，才能为该次明确硬件与配置的运行记录容量结论；不得外推到生产。

## 单位与矩阵

矩阵必须恰好包含四格，不能缺失或重复：

| profile | `budget_encoded_chars` | worker 故障阶段 |
| --- | ---: | --- |
| baseline | 2,097,152 | 有积压时优雅 drain，不重启 |
| baseline | 8,388,608 | 有积压时优雅 drain，不重启 |
| weak | 2,097,152 | 有积压时重启并恢复 |
| weak | 8,388,608 | 有积压时重启并恢复 |

2 MiB 与 8 MiB 在这里分别表示 **2 × 1024 × 1024 和 8 × 1024 × 1024 个 base64url 编码字符**，不是 gzip 字节，也不是密文、JSON 或磁盘字节。浏览器应分别记录：

- `accepted_encoded_chars_total`：已获逐事件 accepted 回执的 payload 的实际 base64url 字符数总和；
- `max_session_encoded_chars`：单会话实际计入预算的 base64url 字符数；
- `accepted_gzip_bytes_total`：base64url 编码前 `CompressionStream(gzip)` 输出字节数总和；
- `largest_unsent_payload_encoded_chars`：因加入后会越过预算而未发送的下一 payload 的编码字符数。

不要用 `gzip_bytes × 4/3` 反推预算使用量。无 padding 的 base64url 长度还受每个独立 payload 的余数影响，分段、JSON、加密 padding 和存储字节又是其他口径。门禁只用实际编码字符串长度检查 2/8 MiB 预算；gzip 体积单独报告，不参与字符预算比较。至少一个会话必须到达边界，且 `max_session_encoded_chars + largest_unsent_payload_encoded_chars > budget_encoded_chars`，同时不得有预算 overshoot。

`weak` 必须是同预算 baseline 的真实弱资源对照：API CPU/内存与 content-worker CPU/内存四项都不能高于 baseline，至少一项必须更低。报告保存每项明确数值，不允许只写“弱机”。每格使用新合成项目；同一格内的浏览器、ClickHouse、PostgreSQL、分页和资源观测必须来自同一次运行。

## 隔离与禁止项

执行观测只能使用显式准备的本地 benchmark Compose 环境：

1. API 为无用户信息、query、fragment 或路径的 literal loopback HTTP origin，例如 `http://127.0.0.1:8081`。
2. 依赖命名空间必须是新生成的 `evia_bench_<12 lowercase hex>`；Kafka topic、consumer group、Redis prefix 和 ClickHouse database 都绑定该命名空间。
3. 项目和账号只包含合成数据，environment 固定为 development。隐私 canary 是专门构造的合成字符串，不得使用账号密码、cookie、token 或业务内容。
4. 不连接生产域名、生产数据库、生产对象存储或生产凭据；不在生产创建任何写入。
5. 原始观测和汇总报告保存在仓库外 `/tmp`，是 regular mode `0600` 文件，不使用 symlink，也不提交。汇总报告应保持在 64 KiB 内；大体积逐请求日志另行私有归档。
6. 评估输出必须是 `/tmp` 下尚不存在的新路径。工具以 `O_EXCL`、`O_NOFOLLOW`（平台支持时）和 mode `0600` 创建，不覆盖旧证据。

CLI 在读取输入或创建输出前先验证 loopback origin 与专用 namespace；报告内二者还必须与命令参数逐字一致。评估器无网络代码，不能把它作为执行流量的入口。

## 观测合同

顶层报告使用以下固定元数据：

```json
{
  "report_version": 1,
  "kind": "evia_session_replay_capacity",
  "synthetic": true,
  "production_writes": false,
  "environment": "development",
  "api_url": "http://127.0.0.1:8081",
  "namespace": "evia_bench_012345abcdef",
  "scenarios": []
}
```

每个 scenario 必须包含以下分组。数值必须来自原始观测，不得用后续数据库值回填浏览器值。

### 接收、CH、PG 与 artifact 对账

- `browser.accepted_chunks`：逐事件响应为 accepted 的加密 chunk 数。HTTP 202 本身不计 accepted；`receipt_rejections` 和 `unknown_outcomes` 必须为 0，HTTP 状态只允许出现正数的 202。
- `browser.accepted_items`：该次浏览器运行中，全部 chunk 都获 accepted 的完整 item 数。
- `clickhouse.distinct_chunks` / `distinct_items`：按完整 chunk identity 和 item identity 去重后的数量；`plaintext_content_rows` 必须为 0。
- `postgres.chunks` / `items` / `artifacts`：worker 完成后的索引 chunk、完整 item 和校验成功 artifact 数。

通过条件为：

```text
browser accepted chunks == CH distinct chunks == PG chunks
browser accepted items  == CH distinct items  == PG items == PG artifacts
```

两组数量都必须大于 0。只比总行数不够；原始私有证据还应保留每格用于 distinct 的复合 identity、artifact 完整性校验结果和查询时间范围。

### 隐私 canary

每格至少注入两个合成 canary（例如 password input 与显式 mask 区域各一个）。报告只保存 canary 数量和三处扫描命中数，不保存 canary 原文：

- `browser_request_properties`：浏览器 collect request properties；
- `clickhouse_properties`：CH properties/ciphertext 旁的可查询字段；
- `artifact_plaintext`：授权解密、重组、gzip 解压后的完整 artifact。

三处命中数都必须为 0。artifact 扫描必须发生在授权解密和完整重组之后；只扫密文没有意义。

### worker drain 与 restart

baseline 在 worker 尚有正数 pending chunks 时请求优雅停止，要求 drain 完成、`pending_after_recovery=0`、无重启。weak 在尚有正数 pending chunks 时重启 content-worker，要求至少一次 restart、恢复完成、最终 pending 为 0。两类都要求 `duplicate_chunks_after_recovery=0`，并在恢复后再做 CH/PG/artifact 精确对账。

`worker.transition_ms` 从 drain/restart 动作开始计到最终 pending 为 0 且 artifact 对账完成。不得只记录进程启动时间。

### 冻结分页及 owner/machine 一致性

每格先用第一页响应的 `as_of` 固定视图，再在同一合成项目追加至少一个完整 item，然后继续至少第二页：

- owner session 和具有该 environment `content.read` 的 machine credential 都必须返回 200；
- 每个 principal 的所有页只能观察同一个 `as_of`；
- `items == distinct_items == frozen_expected_items`，duplicate、missing、cursor error 都为 0；
- `frozen_expected_items + appended_after_freeze_items ==` 最终 PG item 数；
- owner 与 machine 对冻结 item identity 集合计算的 SHA-256 必须相同。

摘要只保存 item identity 集合的 lowercase SHA-256，不保存授权 cookie、Bearer secret 或内容正文。分页延迟分别记录 owner/machine；权限相等不代表认证路径相同。

### 延迟、RSS 与资源配置

每格明确记录 API 与 content-worker 的 CPU millicores 和内存限制。延迟采用 nearest-rank p95，至少 30 个样本，并保留样本数、p95 和 max：

| profile | collect accepted p95 | accepted→artifact ready p95 | owner/machine page p95 | worker drain/restart | API / worker peak RSS |
| --- | ---: | ---: | ---: | ---: | ---: |
| baseline | ≤ 2,000 ms | ≤ 120,000 ms | 各 ≤ 2,000 ms | ≤ 120,000 ms | 各 ≤ 2 GiB |
| weak | ≤ 5,000 ms | ≤ 300,000 ms | 各 ≤ 5,000 ms | ≤ 300,000 ms | 各 ≤ 3 GiB |

这些是首版固定验收上限，不是实测值或生产 SLO。延迟失败不能从分位数样本中删除。RSS 对两个指定 PID 至少各采 30 个同频样本；记录的是 process peak RSS bytes，不把宿主机或容器总内存冒充进程 RSS。退出、重启造成的 PID 切换要继续跟踪新 worker PID，并把同一场景所有 worker 世代的峰值取最大值。

## 评估

先将外部观测器生成的**汇总** JSON 写为 `/tmp` 下 mode `0600` 文件，再运行：

```sh
python3 tooling/benchmark/session_replay_capacity.py \
  --api-url http://127.0.0.1:8081 \
  --namespace evia_bench_012345abcdef \
  --input /tmp/replay-capacity-summary.json \
  --out /tmp/replay-capacity-evaluation.json
```

退出码：

- `0`：四格及所有门禁通过；
- `1`：输入边界有效，但至少一个证据门禁失败；
- `2`：路径、权限、JSON、loopback、namespace 或其他本地验证失败。

失败评估同样会写入新私有输出，以保留失败基线；边界无效时不会创建输出。运行纯政策单测：

```sh
TMPDIR=/tmp python3 -m unittest discover -s tooling/benchmark -p 'test_*.py'
```

归档时同时保留失败与通过轮次、二进制 SHA-256、浏览器/worker 版本、资源限制、宿主硬件、采样间隔、场景起止时间和原始私有证据。不要新增 verification 结果文档，除非真实四格运行已完成且证据可复核；不要将本工具的 unit pass 描述成容量实测。
