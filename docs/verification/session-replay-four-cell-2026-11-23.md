# 会话回放 2/8 MiB × baseline/weak 四格实测（2026-11-23）

## 结论

四格均执行了真实浏览器→collector/Kafka→ClickHouse→content-worker→PostgreSQL/artifact 路径。SDK 原生 bulk drain、accepted 后预算提交、弱网配置续传、429 Retry-After、worker hold/drain/restart 均取得实证；但**整份 evaluator 仍应判失败**，不能宣称四格容量全部通过：

1. 2 MiB 两格与 8 MiB baseline 没有由 fixture 明确记录 `largest_attempted_rejected_payload_encoded_chars`，也没有 accepted/reserved/pending 最终 checkpoint，因此这些格的预算边界证据不满足 evaluator 固定合同，尽管使用量接近上限且没有 overshoot。
2. 原 600-byte 8 MiB weak 在固定观测窗只 accepted 6,399,837 / 8,388,608 encoded chars（76.3%）；后续 4 KiB 重跑已以 accepted=8,103,551、reserved/pending=0、下一份实际 rejected payload=525,514 证明跨越预算边界，并将 chunks 降至 2,037，但这只核销 weak 8 单格的吞吐/边界缺口。
3. baseline 8 MiB 首次 fixture 在浏览器 close 时留下 1 个 2/28 chunk 的未跟踪尾项；发现 API正确排除它，后续 fixture 已改为在页面存活时显式触发一次性 pagehide 并等待稳定，但该格未重新跑完整 8 MiB。
4. 本机 ClickHouse 依赖盘最大仅约 512 MiB，低于 runbook 2 GiB reserve，不能满足正式资源环境门禁。
5. RSS、artifact-ready p95 与 owner/machine page p95 没有取得至少 30 个同频样本，正式 evaluator 必须失败。

## SDK 与 producer 修复

- SDK `emitEncryptedContentChunks()`：完整列表先按事件数/字节预算原子校验，不留下隐藏前缀。
- SDK `drainUntilSettled(Emission[])`：跨单次 flush 四批上限，仅按调用方 handles 判断；timeout/maxRounds/AbortSignal 有界；429/Retry-After 不消耗轮次；并发调用共享 collection single-flight。
- SDK 89/89：覆盖 >4 batches、queue pressure、max-round、429、ambiguous failure、timeout/cancel、concurrent callers、unrelated queue、persist/reload 和 maxAttempts。
- newSSE 使用 vendored native API；事件 lifetime 15 分钟，replay drain 10 分钟，16k segment 保持 ≤32 encrypted chunks/item，三 item 组保留队列余量，request 上限仍受 60 KiB keepalive byte cap。
- `used_bytes` 只在整组 emission outcomes 全部 `accepted` 后按实际 segment data 字符提交；配置 503 / 未入队 / pending/rejected 不预扣。弱网首次 content batch 被合成 429 时 `usedAt429=0`，最终 202 后才增加。
- 动态 SDK + 签名配置首载等待从 5 秒放宽到固定 30 秒；250ms / 32 KiB/s 下 config 200，未再因配置晚到永久停录。

## 4 KiB replay 模式补充验证

为降低 600-byte 固定块造成的事件放大，平台新增可选 `fixed-4096-v2`；newSSE 仅会话回放显式选择 4096，Smart XiaoQiu 等通用敏感内容继续走默认 `fixed-600-v1`。真实 E2E 首跑暴露了单测没有覆盖到的两个跨层门禁：pipeline 的通用属性字符串上限 1024 会在 durable queue 前拒绝 5483-char ciphertext；FileStore 的读取上限仍是 600 bytes，会让已经解密落盘的 4 KiB item 在授权 API 返回 500。修复分别限定在 `sys.content_chunk.ciphertext` 和认证后的 `MaxPlaintextChunkLength=4094`，没有放宽普通业务属性；Put/Read 和 queue encode/decode 均新增上限与超限拒绝测试。

修复后复跑 newSSE 浏览器→collector/Kafka→ClickHouse→content-worker→PostgreSQL/artifact→授权 API：

- 浏览器实际请求全部 `padding=fixed-4096-v2`、`ciphertext.length=5483`、HTTP 202、telemetry header 存在、无 plaintext `content` 属性。
- 代表流 ClickHouse 24/24 distinct chunks、9 items，PostgreSQL 24 chunks / 9 items / 24 distinct object keys；全部 artifact 可读，密码 mask canary 全目录扫描为 0。
- 同一 9459-byte replay mutation item 未授权读取 401，owner `content.read` 200，重组内容 SHA-256 为 `e0a7381502330c6fd9d7d78c6cb5e4977795025336878088e678f81e29edb48c`。
- SDK 92/92，newSSE typecheck + production build、focused Go content/pipeline、真实 Kafka+ClickHouse integration 通过。

这证明新模式能完整穿过真实数据路径。随后按相同 55-wave workload 和 250ms / 32 KiB/s weak profile 重跑 8 MiB 格，并增加 60 waves 复跑与显式预算 checkpoint：

| 8 MiB weak | accepted chars | boundary | wall | HTTP batches | content chunks | p95/max | 429 |
|---|---:|---:|---:|---:|---:|---:|---|
| 600-byte 原格 | 6,399,837 | 76.3% | 1160.049s | 534 | 11,198 | 2157/2202ms | 429→202；usedAt429=0 |
| 4 KiB / 55 waves | 8,105,140 | 96.62% | 989.533s | 345 | 2,037 | 2014/2063ms | 429→202；usedAt429=0 |
| 4 KiB / 60 waves + evidence | 8,103,551 | 已真实触发边界拒绝 | 988.309s | 345 | 2,037 | 2014/2060ms | 429→202；usedAt429=0 |

同 workload 下 accepted 提升 26.6%，content chunks 减少 81.8%，HTTP batches 减少 35.4%，wall 降低 14.7%，p95 降低 6.6%。60-wave 证据格最终 checkpoint 为 accepted=8,103,551、reserved=0、pending=0；下一份实际 payload 为 525,514 chars，且 `8,103,551 + 525,514 > 8,388,608`，因此不是“没生成够数据”，而是确实命中 whole-payload 预算拒绝。ClickHouse / PostgreSQL / distinct artifact objects 均为 2,037，512 complete items，全部 ciphertext 5483 chars 且 padding 为 `fixed-4096-v2`。

4 KiB 已修复旧 8 MiB weak 格的吞吐与预算边界缺口，但**整份正式 evaluator 仍保持 FAIL**：本机 ClickHouse 仅余约 33.5 MiB，低于 2 GiB reserve；该容量 workload 没有重新注入双 canary，也没有补齐至少 30 个同频 RSS/artifact-ready/owner-page/machine-page 样本和 shared first-page freeze。下面四格表保留原 600-byte 历史结果，不能把新的单格通过改写成整份矩阵 PASS。

## 浏览器 capture 结果

预算单位均为 gzip 后 base64url encoded characters，不是 gzip bytes。

| cell | accepted chars | budget | boundary ratio | wall | collect | collect p95/max | 429 | warning |
|---|---:|---:|---:|---:|---:|---:|---:|---|
| 2 MiB baseline | 1,912,311 | 2,097,152 | 91.2% | 233.732s | 160×202 | 18/75ms | no | 0 |
| 2 MiB weak | 1,971,677 | 2,097,152 | 94.0% | 463.927s | 168 responses | 2155/2200ms | injected, retried 202; usedAt429=0 | 0 |
| 8 MiB baseline | 8,232,565 | 8,388,608 | 98.1% | 674.867s | 692×202 | not captured | no | 0 |
| 8 MiB weak | 6,399,837 | 8,388,608 | 76.3% | 1160.049s | 534 responses | 2157/2202ms | injected, retried 202; usedAt429=0 | 0 |

Weak profile：250ms latency，32,768 B/s 上下行。弱网 collect p95 均低于 5,000ms 固定 gate；2 MiB baseline p95 低于 2,000ms。8 MiB baseline 缺少 latency samples，不补推断值。

## 仓库对账与隐私

| representative stream | profile/budget | CH complete/raw items | CH chunks | PG items | PG chunks | distinct artifact objects | plaintext rows |
|---|---|---:|---:|---:|---:|---:|---:|
| `85817ac0-...` | 2 baseline latest | 122 | 3,347 | 122 | 3,347 | 3,347 | 0 |
| `b1077016-...` | 2 weak | 127 | 3,450 | 127 | 3,450 | 3,450 | 0 |
| `e1f8b1ef-...` | 8 baseline | 519 complete / 520 raw | 14,429 | 520 raw | 14,429 | 14,429 | 0 |
| `44ecae2e-...` | 8 weak full observation | 406 | 11,198 | 406 | 11,198 | 11,198 | 0 |

8 baseline 的单个 raw 尾项为 fixture close 产生的 2/28 chunks；content discovery 按完整性过滤后 owner/machine 均只见 519 complete items，不把残项伪装成可播放内容。

旧的 mask canary 全 artifact 扫描为 0；本次四格 workload 未重新注入独立双 canary，因此正式 privacy contract 的 `canary_count>=2` 仍判缺证，不用 `plaintext rows=0` 替代。

## content-worker drain / restart

优化前 worker 每 200 chunks 固定等待 5 秒，8 baseline 14,429 chunks drain 为 377.627s。修复后满 200-chunk scan 立即续跑，partial/empty/error 才等待原 interval：

- 2 baseline latest 3,347 chunks：31.825s（≤120s）。
- 2 baseline representative 3,456 chunks：22.932s（≤120s）。
- 8 baseline 14,429 chunks：111.122s（≤120s）。
- 8 weak full 11,198 chunks：167.535s（≤300s）。
- 2 weak restart drill：首段 584 chunks / 4.079s；离线 10s cursor/rows 逐字节不变；restart 剩余 drain 23.599s，整体远低于 300s。
- 8 weak restart drill（6.4M 同 profile 代表流）：首段 584 chunks / 3.594s；离线 10s cursor/rows不变；restart drain 62.805s。

PG insert `ON CONFLICT DO NOTHING`、artifact hash object key 与 cursor lease 保证重扫没有重复 key 或 hash 漂移。

## Owner / machine 授权与分页

实测创建 environment-bound `content.read` machine 后，owner session 与 machine bearer 对四条代表流走完整签名 cursor 分页：

- 8 baseline：6 pages / 519 complete items，item-set SHA-256 相等。
- 8 weak：5 pages / 406 items，SHA-256 相等。
- 2 baseline：2 pages / 126 items，SHA-256 相等。
- 2 weak：2 pages / 127 items，SHA-256 相等。

过程中发现 PostgreSQL `machine_principals_permissions_check` 未随 domain/OpenAPI 新增 `content.read` 扩展，API 500 且事务回滚。迁移 049 将闭集扩为 27 项；真实 `-race` machine grant round-trip 通过，API 创建 201，随后分页集合严格相等。

当前 cursor 仍绑定 actor，owner/machine 首次请求分别生成 `as_of`；在无并发 append 的最终对账下集合相等。正式 harness 要证明相同 freeze instant，仍需 fixture shared-clock/attestation，不能把本次静止后相等替代冻结并发证明。

## Harness 与剩余失败

可执行 harness 已支持 private 0600 settings / 0700 checkpoints、baseline/weak × 2/8 MiB、七 hook、resume、dry-run、loopback/namespace/mode/command/output fail-closed。根据本次缺口，后续 v2 证据合同还明确区分 accepted/reserved/pending budget checkpoint、最大实际预算拒绝 payload、capture wall 与 collect ack、pagehide/context close 顺序、raw incomplete/discovery exclusion、CH disk reserve、owner/machine 首次共享冻结、双 canary 注入及 worker generation/restart checkpoints；artifact 仍严格按 chunk 对账，不改成按 item。

当前缺 caller fixture wrapper 对上述字段和 RSS/p95 的完整观测接线，因此本报告是实机四格证据，不伪装成 evaluator PASS 报告。既有手工每格观测可用 harness `--assemble-fragments` 组装并让缺证逐字段失败，无需手写或补造顶层 JSON。下一步应补观测 wrapper 并重跑失败格，而不是放宽门禁。
