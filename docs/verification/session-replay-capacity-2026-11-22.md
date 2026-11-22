# 会话回放 2 MiB 基线容量实测（2026-11-22）

## 范围

本次只运行 default 2 MiB、loopback baseline 单格；不代表 8 MiB 或弱网通过。预算单位为 gzip 后 base64url **编码字符数**，不是 gzip bytes。

fixture 在 newSSE 登录页注入 100 条/波、每条 3000 hex 字符的高熵 DOM child mutation；同时放置 `data-evia-mask` canary。真实 browser SDK、collector、Kafka、ClickHouse、content-worker、artifact/PG 全链运行。

## 正确性结果

- 配置预算：2,097,152 encoded chars。
- 最终计费：2,013,320；剩余 83,832。
- 最近成功 payload 增量：218,831；下一同尺寸 payload 会超预算，录制按“下一 payload 不完整则整批拒绝”停止，未越界。
- 浏览器 accepted item：153。
- ClickHouse：153 distinct items / 2,567 `sys.content_chunk` rows。
- PostgreSQL：153 complete items / 2,567 chunks。
- collect：所有响应 202；ClickHouse 明文 `content` 字段为 0。
- 受保护 artifacts 全量扫描未出现 `CAPACITY_MASK_CANARY_MUST_NEVER_APPEAR`。

因此默认预算下 item/chunk 对账、完整 item 过滤、明文边界和预算停止语义通过。

运行期间 ingest-worker 两次在长时间运行后出现 `facts` 阶段 11 次重试并自停，同时 readiness receipt/renewal 失败；ClickHouse 容器健康且本次目标流已完整对账。根因之一是 readiness ProbeWorker 仅重试20秒便返回，supervisor 随即取消仍可恢复的 fact worker。修复后 readiness batch 保持未提交并持续重试到依赖恢复或进程 context 取消；本机将 ClickHouse pause 25秒（超过旧上限）再恢复，ingest-worker 未退出并继续 renewal。旧 release 的单项 renewal 仍有失败告警，需要后续清理/分类，但不会再因 probe 短暂超时拖停整个进程。

## 性能结果与失败门禁

- wall time：436.511 s。
- collect batches：153；总事件 2,571（其中 content chunks 2,567）。
- 现有 baseline capture gate 目标为 120 s；本次 **失败**，约 3.64× 超限。
- 主因是 correctness-first 路径逐 replay item 显式 flush 并等待 accepted；一个约 218k encoded-char payload 展开约 22 items，形成大量串行 HTTP 往返。

尝试过 8-item 并发 enqueue/flush 探针：wall time 降到 184.951 s，但预算已计 2,013,246 时只确认 9 items / 682 total events，暴露并发 flush/queue 竞争导致的严重完整性退化。该实验改动已立即回滚，未提交。这个结果证明不能以吞吐换取静默丢段。

随后实现安全 bulk：分离 encryption enqueue 与确认，先单项建立 content config，再每组最多3 items（约54 events，给200-event/256KiB队列留余量），SDK 最多64轮有界 drain，并逐 emission outcome accepted 后记 item。复跑结果：2,016,232/2,097,152 encoded chars，ClickHouse/PostgreSQL 同为205 items / 3,436 chunks，全部 collect 202；wall 326.124s，比436.511s快25.3%，但仍是120s gate的2.72×，因此性能结论仍为失败。安全优化已保留，8-item丢段方案未保留。

## 弱网探针

Chromium 移动上下文 `390×844` 使用 250 ms latency、32 KiB/s 上下行运行登录页 snapshot+100 mutations。保持页面 168.169 s 后，预算已计 56,016 encoded chars，但 accepted item 仍为0；因此 weak capture gate 失败，预定的首批 429/Retry-After 注入尚未进入可验证阶段。该结果要求先把 config/collector request timeout、持久队列续传和“预算计费早于 accepted”可观测性补齐，再运行完整 weak 四格，不得把静默等待视为通过。

## 下一步

在运行耗时约四倍的 8 MiB 矩阵前，先扩展 SDK 为显式 bulk emission/drain API：在单队列所有 emission 已取得句柄后，按 20-event batch 循环 flush，并逐 outcome 对账；不得并发调用现有 per-item flush。优化必须先复现 browser=CH=PG=artifact 等值，再运行 baseline/weak × 2/8 MiB 四格矩阵。
