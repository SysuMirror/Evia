# 会话回放容量基准

本切片包含两个边界清晰的工具：`tooling/benchmark/session_replay_harness.py` 负责调用**外部提供的**专用 loopback full-stack/newSSE fixture hooks、按格保存断点并生成评估器输入；`tooling/benchmark/session_replay_capacity.py` 仍只做纯核对与固定通过门禁。仓库不提供 fixture 可执行文件、凭据或生产连接；harness 不修改浏览器 SDK 或业务 app producer，也不把普通 newSSE development mode 当作容量 fixture。

本文没有新增实测结果。dry-run 或单元测试通过只证明配置、命令合同和报告政策，不能当作浏览器、数据库、worker 或生产容量证据。只有在专用本地合成环境完成全部矩阵、保留原始观测并由评估器返回 `passed: true` 后，才能为该次明确硬件与配置的运行记录容量结论；不得外推到生产。

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
- `browser.budget_checkpoint.accepted_encoded_chars`：最终已获 accepted outcome、已经提交的单会话预算；必须等于 `max_session_encoded_chars`；
- `browser.budget_checkpoint.reserved_encoded_chars` / `pending_encoded_chars`：最终 checkpoint 尚处于本地预留或回执未决的字符数；二者必须为 0，不能混入 accepted 口径；
- `largest_attempted_rejected_payload_encoded_chars`：fixture 实际尝试加入、因会越过本地字符预算而被拒绝的最大 payload 编码字符数。不得用“最近一次成功 payload”或后续数据库行大小代填。

不要用 `gzip_bytes × 4/3` 反推预算使用量。无 padding 的 base64url 长度还受每个独立 payload 的余数影响，分段、JSON、加密 padding 和存储字节又是其他口径。门禁只用实际编码字符串长度检查 2/8 MiB 预算；gzip 体积单独报告，不参与字符预算比较。至少一个会话必须到达边界，且 `budget_checkpoint.accepted_encoded_chars + largest_attempted_rejected_payload_encoded_chars > budget_encoded_chars`，同时不得有预算 overshoot。只记录“尚未发送”但没有实际预算拒绝观测不算边界证据。

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
  "report_version": 2,
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
- `clickhouse.distinct_chunks` / `distinct_items`：按完整 chunk identity 去重后的 chunk 数，以及只计 chunk 集合完整的 item 数；`raw_items` 是不论完整性的 raw item identity 数，`incomplete_raw_items` 是其中 chunk 集合不完整的数量；`plaintext_content_rows` 必须为 0。
- `postgres.chunks` / `items` / `artifacts`：worker 完成后的索引 chunk、完整 item 和校验成功 artifact 数；`raw_items` / `incomplete_raw_items` 使用与 CH 相同的完整性口径。
- `pagination.discovery_excluded_incomplete_items`：发现 API 排除的 raw 不完整 item 数，必须等于 CH/PG 的 incomplete 数。0 也必须显式报告，不能省略。

通过条件为：

```text
browser accepted chunks == CH distinct chunks == PG chunks == PG artifacts
browser accepted items  == CH distinct complete items == PG complete items
CH/PG raw items         == complete items + incomplete raw items
CH incomplete           == PG incomplete == discovery excluded incomplete
```

artifact **始终是一条已解密 chunk 对应一个不可变对象**，不是每个 item 一个 artifact；即使 raw item 不完整，其已落地 chunk 仍逐 chunk 对账。两组 accepted 数量都必须大于 0。只比总行数不够；原始私有证据还应保留每格用于 distinct 的复合 identity、artifact 完整性校验结果和查询时间范围。

### 隐私 canary

每格至少注入两个合成 canary（password input 与显式 mask 区域各一个）。`privacy.canary_injection` 必须逐项证明 `password_input_injected=true`、`masked_region_injected=true`、`injected_before_capture=true`；只报告后续扫描为 0、沿用旧轮次 canary 或持有 harness seed 都不能代替本格实际注入。报告只保存注入 attestation、canary 数量和三处扫描命中数，不保存 canary 原文：

- `browser_request_properties`：浏览器 collect request properties；
- `clickhouse_properties`：CH properties/ciphertext 旁的可查询字段；
- `artifact_plaintext`：授权解密、重组、gzip 解压后的完整 artifact。

三处命中数都必须为 0。artifact 扫描必须发生在授权解密和完整重组之后；只扫密文没有意义。

### worker drain 与 restart

baseline 在 worker 尚有正数 pending chunks 时请求优雅停止，要求 drain 完成、`pending_after_recovery=0`、无重启。weak 在尚有正数 pending chunks 时重启 content-worker，要求至少一次 restart、恢复完成、最终 pending 为 0。两类都要求 `duplicate_chunks_after_recovery=0`，并在恢复后再做 CH/PG/artifact 精确对账。

每格还必须在动作前后记录 `generation_before_transition`、`generation_after_recovery`、`restart_count_before_transition`、`restart_count_after_recovery`。`restart_count` 必须严格等于两个 checkpoint 的差；baseline generation 与 restart count 均不得变化，weak generation 必须变化且 restart count 至少增加 1。进程“曾经启动”或只看最终 PID 不构成 restart/recovery 证据。

`worker.transition_ms` 从 drain/restart 动作开始计到最终 pending 为 0 且 artifact 对账完成。不得只记录进程启动时间。

### 冻结分页及 owner/machine 一致性

每格先用第一页响应的 `as_of` 固定视图，再在同一合成项目追加至少一个完整 item，然后继续至少第二页：

- owner session 和具有该 environment `content.read` 的 machine credential 都必须返回 200；
- owner/machine 分别保存 `first_page_frozen_at`，二者必须逐字等于 scenario 的 `frozen_at`，并显式声明 `shared_first_page_freeze=true`；静止数据集上最终集合碰巧相等不能代替共享首次冻结证明；
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

这些是首版固定验收上限，不是实测值或生产 SLO。延迟失败不能从分位数样本中删除。`capture.wall_time_ms` 单独保存整段浏览器 capture wall time，**只报告、不参与通过门禁**；它不能写进 `latency.collect_ack`，也不能拿整段 wall time 与 collect ack p95 上限比较。如果以后增加 wall gate，必须先在 evaluator 固定一个显式且宽裕的 profile 上限，不能由报告自行选择。`capture.pagehide_triggered`、`pagehide_finalized`、`pagehide_finalized_before_context_close` 必须全为 true，证明 fixture 在关闭 browser context 前主动触发一次性 pagehide、等待预算/队列稳定并完成最终化。

RSS 对两个指定 PID 至少各采 30 个同频样本；记录的是 process peak RSS bytes，不把宿主机或容器总内存冒充进程 RSS。退出、重启造成的 PID 切换要继续跟踪新 worker PID，并把同一场景所有 worker 世代的峰值取最大值。每格还要在 workload 前后观测 `clickhouse.disk_reserve_bytes_before` / `disk_reserve_bytes_after`；两者都不得低于 evaluator 固定的 2 GiB reserve。容器内存限制、宿主总盘或配置声明不能代替 ClickHouse 实际可用 reserve 观测。

## 可执行四格 harness

### 外部 fixture 合同

harness 只编排调用方提供的可执行 hook，不包含或修改 newSSE producer。fixture 必须是独立 full-stack 实例，并同时满足：

- API 与 fixture control origin 都是无路径、query、fragment、userinfo 的 literal loopback HTTP origin；
- `fixture_kind` 固定为 `full_stack_new_sse`，`new_sse_mode` 固定为 `replay_capacity_fixture`；普通 `development` 模式会在任何 hook 执行前拒绝；
- namespace 是新分配的 `evia_bench_<12 lowercase hex>`，fixture 把 Kafka topic/group、Redis prefix、ClickHouse database 和本次合成项目全部绑定到它；
- 每格 `prepare_cell` 都创建新合成项目并返回边界 attestation，不能复用业务项目；
- hook 只读取 `EVIA_REPLAY_*` 环境。harness 不继承 `EVIA_E2E_PASSWORD`、proxy、云凭据或其他 ambient environment，且用 argument array 直接执行，不经过 shell。

harness 为每格确定性传入：namespace、cell ID、profile、2/8 MiB encoded-character budget、SHA-256 seed、两个合成 canary、资源 JSON 与网络 JSON。固定网络参数为：

| profile | latency | download | upload | packet loss |
| --- | ---: | ---: | ---: | ---: |
| baseline | 0 ms | unlimited (`-1`) | unlimited (`-1`) | 0% |
| weak | 250 ms | 32,768 B/s | 32,768 B/s | 0% |

弱网不是调用方可放宽的报告字段。fixture 的浏览器 workload 必须实际应用 `EVIA_REPLAY_NETWORK_JSON`，按 `EVIA_REPLAY_BUDGET_ENCODED_CHARS` 执行，并把 harness 提供的 password/masked canary 放入对应合成 DOM；canary 原文不得写入报告。

固定 hook 顺序为：

```text
prepare_cell → worker_hold → browser_workload
  → baseline: worker_drain / weak: worker_restart
  → frozen_pagination → reconcile
```

各 hook stdout 只能是一个不超过 64 KiB 的 JSON object；stderr 同样有 64 KiB 上限。退出非零、超时、非 JSON、字段重叠或不完整都会使该格在写 checkpoint 前失败。hook 响应合同为：

- `prepare_cell`：逐字返回 api/fixture URL、namespace、cell ID、fixture kind/mode，并声明 `synthetic=true`、`production_writes=false`、`dedicated_namespace=true`、`ordinary_development_mode=false`、`fresh_project=true` 和一个有界 `project_fixture_id`；
- `worker_hold`：仅返回 `{"hold_armed": true}`；
- `browser_workload`：返回且只返回 `checkpoint`、`browser`、`capture`、`privacy` 四个 object。`checkpoint` 包含正数 `accepted_items_before_freeze` / `accepted_chunks_before_transition`；`browser` 提供 accepted/reserved/pending budget checkpoint 与 largest attempted rejected payload；`capture` 提供 wall time 和三个 pagehide 证明；`privacy.canary_injection` 提供本格双 canary 注入证明。这三组 evidence 会先并入 scenario，后续 hook 重复同一叶子会 fail-closed；
- `worker_drain` / `worker_restart`：返回评估器所需完整 `worker` object，且对应 pending-before 值为正，并包含 transition 前后 generation/restart checkpoints；transition 时间必须覆盖恢复到 pending=0 及 artifact 完整就绪；
- `frozen_pagination`：用 fixture shared clock 取得 owner/machine 相同首页 `as_of`，保存两条 first-page timestamp 与 shared attestation，追加至少一个完整 item，再走完两种认证的冻结分页；返回累计最终 `browser`、`pagination`、collect/page `latency` 与 browser privacy scan；累计 browser 数不能低于前阶段；
- `reconcile`：在追加 item 和 worker 恢复之后返回 `clickhouse`、`postgres`、artifact-ready `latency`、CH/artifact privacy scan 与跨 worker 世代 `rss`。这里必须做 CH distinct/raw/incomplete identity、PG complete/raw/incomplete chunk/item、逐 chunk artifact、实际 ClickHouse disk reserve、授权解密重组/hash 和 artifact 明文 canary 扫描，不能只抄一个数据库总数。

冻结值必须来自实际签名 cursor，不得在报告中改写。当前公开内容 API 为 owner 和 machine 的首次请求各自读取当前时间，且 cursor 绑定 actor；因此普通服务模式不能保证两者得到逐字相同的 `as_of`。调用方 fixture 仍需提供一个**仅用于该专用 loopback fixture** 的共享时钟钉住/等价测试能力，然后分别走真实 owner session 与 machine Bearer 路径；harness 本身没有这项后端接线，也不会伪造或跨 actor 复用 cursor。

完整叶子字段和门禁仍以本页“观测合同”与 evaluator 为准。harness 从私有设置注入 `resources`，将两个 privacy/latency 分段无歧义合并，最终输出 evaluator 当前接受的 `evia_session_replay_capacity` v2 JSON。

### 私有设置与精确命令

先由运维者安装或构建自己的 fixture wrapper；下面的 `/opt/evia-replay-fixture/bin/replay-fixture` 是必须替换为**实际绝对 executable 路径**的占位，仓库没有声称它已存在。创建专用 checkpoint 目录和设置文件：

```sh
umask 077
install -d -m 0700 /tmp/evia-replay-capacity-cells
install -m 0600 /dev/null /tmp/evia-replay-capacity-settings.json
${EDITOR:?set EDITOR to a local editor} /tmp/evia-replay-capacity-settings.json
```

设置内容如下；资源值是本次运行的显式 fixture 限制，不是测量结果，weak 四项不得高于同预算 baseline 且至少一项更低：

```json
{
  "harness_version": 2,
  "kind": "evia_session_replay_harness",
  "synthetic": true,
  "production_writes": false,
  "environment": "development",
  "api_url": "http://127.0.0.1:8081",
  "fixture_url": "http://127.0.0.1:4174",
  "fixture_kind": "full_stack_new_sse",
  "new_sse_mode": "replay_capacity_fixture",
  "namespace": "evia_bench_012345abcdef",
  "report_dir": "/tmp/evia-replay-capacity-cells",
  "hook_timeout_seconds": 21600,
  "resources": {
    "baseline": {
      "api_cpu_millicores": 2000,
      "api_memory_bytes": 2147483648,
      "content_worker_cpu_millicores": 2000,
      "content_worker_memory_bytes": 2147483648
    },
    "weak": {
      "api_cpu_millicores": 1000,
      "api_memory_bytes": 1073741824,
      "content_worker_cpu_millicores": 500,
      "content_worker_memory_bytes": 1073741824
    }
  },
  "commands": {
    "prepare_cell": ["/opt/evia-replay-fixture/bin/replay-fixture", "prepare-cell"],
    "worker_hold": ["/opt/evia-replay-fixture/bin/replay-fixture", "worker-hold"],
    "browser_workload": ["/opt/evia-replay-fixture/bin/replay-fixture", "browser-workload"],
    "worker_drain": ["/opt/evia-replay-fixture/bin/replay-fixture", "worker-drain"],
    "worker_restart": ["/opt/evia-replay-fixture/bin/replay-fixture", "worker-restart"],
    "frozen_pagination": ["/opt/evia-replay-fixture/bin/replay-fixture", "frozen-pagination"],
    "reconcile": ["/opt/evia-replay-fixture/bin/replay-fixture", "reconcile"]
  }
}
```

先 dry-run。它检查设置 `0600`、checkpoint 目录 `0700`、loopback origins、fixture mode、namespace、资源关系、七个 executable 和 fresh summary path；不会执行 hook、访问网络或创建报告：

```sh
python3 tooling/benchmark/session_replay_harness.py \
  --settings /tmp/evia-replay-capacity-settings.json \
  --out /tmp/replay-capacity-summary.json \
  --dry-run
```

再运行四格。每个完成格以 `O_EXCL`/`0600` 写入 checkpoint；中断后用相同设置重跑，已完成且 settings SHA-256 匹配的格作为输入恢复，不执行其 hook。checkpoint 目录只允许四个规定文件，其他旧输出会被拒绝：

```sh
python3 tooling/benchmark/session_replay_harness.py \
  --settings /tmp/evia-replay-capacity-settings.json \
  --out /tmp/replay-capacity-summary.json
```

退出码 `0` 表示 harness 完成且 evaluator 门禁通过，`1` 表示已写完整 summary 但至少一个容量门禁失败，`2` 表示边界、命令或本地执行失败。`1` 仍应保留证据并继续运行下面 evaluator；不得把它改写成成功。summary 已存在时一律拒绝覆盖；如中断后需要换 summary 名称，使用尚不存在的新路径：

```sh
python3 tooling/benchmark/session_replay_harness.py \
  --settings /tmp/evia-replay-capacity-settings.json \
  --out /tmp/replay-capacity-summary-resume-001.json
```

### 从手工每格 fragment 组装

真实长跑也可以由运维者逐格手工采集，而不必手写顶层 report JSON。创建独立 mode `0700` 目录，放入恰好四个 mode `0600` JSON object；文件名固定为 `baseline-2097152.json`、`baseline-8388608.json`、`weak-2097152.json`、`weak-8388608.json`。每个文件是对应完整 scenario（也接受 harness 原生 cell checkpoint wrapper），必须包含与 settings 完全一致的 `name`、profile、budget、固定 network 和 resources：

```sh
python3 tooling/benchmark/session_replay_harness.py \
  --settings /tmp/evia-replay-capacity-settings.json \
  --assemble-fragments /tmp/evia-replay-manual-cells \
  --out /tmp/replay-capacity-manual-summary.json
```

组装器补齐 v2 顶层边界，按固定矩阵排序，生成可直接交给 evaluator 的私有 report。目录缺格、额外文件、格 identity/network/resource 不符会以退出码 2 明确拒绝且不写 summary；叶子 evidence 缺失或门禁失败则仍写完整 report，以退出码 1 返回，并在 stdout `failures` 中按 cell 列出字段路径（例如缺 pagehide、shared freeze、disk reserve、canary 或 generation checkpoint）。这样保留失败证据，不需要人为补造字段。

## 评估

harness 生成或 fragment helper 组装的**汇总** JSON 已是 evaluator 当前输入合同；也可由符合相同 v2 合同的外部观测器写为 `/tmp` 下 mode `0600` 文件。使用尚不存在的评估输出运行：

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
