# 会话回放 2/8 MiB × baseline/weak 四格实测（2026-11-23）

## 结论

四格均执行了真实浏览器→collector/Kafka→ClickHouse→content-worker→PostgreSQL/artifact 路径。SDK 原生 bulk drain、accepted 后预算提交、弱网配置续传、429 Retry-After、worker hold/drain/restart 均取得实证；但**整份 evaluator 仍应判失败**，不能宣称四格容量全部通过：

1. 2 MiB 两格与 8 MiB baseline 没有由 fixture 明确记录 `largest_unsent_payload_encoded_chars`，因此预算边界证据不满足 evaluator 的固定合同，尽管使用量接近上限且没有 overshoot。
2. 8 MiB weak 在固定 18 分钟观测窗只 accepted 6,399,837 / 8,388,608 encoded chars（76.3%），明确没有到达预算边界。
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

可执行 harness 已支持 private 0600 settings / 0700 checkpoints、baseline/weak × 2/8 MiB、七 hook、resume、dry-run、loopback/namespace/mode/command/output fail-closed；harness/evaluator focused 33/33，完整 benchmark tools 202/202。

当前缺 caller fixture wrapper 对 RSS/p95/canary/shared-freeze 的完整观测接线，因此本报告是实机四格证据，不伪装成 evaluator v1 PASS 报告。下一步只需补观测 wrapper 并重跑失败格，而不是再改 evaluator 放宽门禁。
