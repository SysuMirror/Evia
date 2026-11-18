# 浏览器 SDK 持久离线队列验证记录（2026-11-18）

## 问题（确定性缺口）

浏览器 SDK 的有界队列此前只存在于内存：`StoragePort`（浏览器默认 localStorage）仅持久化匿名 ID，事件队列在页面刷新、同上下文导航或进程终止后全部丢失。`docs/browser-sdk.md` 明确「内存队列不是持久离线存储，不能在进程终止或刷新后存活」。本切片以 opt-in 方式把同一有界队列写穿到存储，补齐刷新/导航后的投递韧性，不新增依赖、不改默认行为。

## 范围与语义决策

1. **opt-in、默认关闭**：新增 `ClientOptions.persistQueue?: boolean`，省略时行为与历史内存队列逐字节一致（现有 57 个测试零改动通过即证据）。
2. **复用现有 StoragePort 与有界预算**：队列快照写入独立键 `evia:project:env:writeKey:queue`，与匿名 ID 键分离；沿用 `maxQueueEvents`（默认 200）/`maxQueueBytes`（默认 256 KiB）上限，天然落在浏览器存储配额内，无需扩展 `getItem/setItem` 端口（队列清空时写回空快照而非 removeItem）。
3. **快照只存可序列化字段**：格式 `evia.queue.v1`，保存 `eventId/draft/createdAt/bytes/attempts/ambiguous` 与可选的就绪 `json`；每个 emission 的 `settle` Promise 属于发起页面，不可跨刷新持久化。
4. **重水化严格裁剪、fail-soft**：构造期读取快照，结构守卫 `isPersistedQueue/isPersistedEntry` 拒绝畸形项，`maxAge` 过期项与超预算项被丢弃，写回裁剪后快照；快照损坏（非法 JSON/错误 format）静默回退到空内存队列，不抛错。
5. **at-least-once 且不产生重复事实**：重水化事件保留原始 `event_id`；采集端本就按稳定 source/event ID 在精确读视图去重，因此刷新后的重投安全幂等，与文档既有「重试可能产生重复回执、服务端去重」模型一致。
6. **无就绪信封的条目再解析**：刷新前尚未拿到配置（无 `json`）的条目，在 `loadConfig` 成功后经抽出的 `resolveQueuedEntries()` 用当前公开配置补全 `schema_version/release_revision`；已下线事件仍 `EVENT_NOT_PUBLISHED` fail-closed。
7. **存储故障软降级**：`getItem/setItem` 抛错被各路径 try/catch 吞掉，回退内存队列，采集不中断。

## 改动清单

| 文件 | 说明 |
| --- | --- |
| `packages/browser-sdk/src/types.ts` | `ClientOptions` 增加 opt-in `persistQueue` |
| `packages/browser-sdk/src/client.ts` | 新增 `PersistedQueue/PersistedEntry` 类型与结构守卫、`persistQueueState()` 写穿、`rehydrateQueue()` 重水化裁剪、`resolveQueuedEntries()` 配置再解析；enqueue/finish/loadConfig 接入；构造器重水化后驱动 init 投递 |
| `packages/browser-sdk/test/client.test.ts` | 新增 6 个测试与 Map 存储夹具 |

重构点：把原 `enqueue` 内联的配置解析逻辑抽为共享 `resolveQueuedEntries()`，同时服务于新 emission 与重水化条目，单触/重试/过期等既有路径不变。

## 验证

### 单元测试（node:test，注入式 TestClock + 假 fetch + Map StoragePort）

`npm test` 全套 63 项通过（原 57 + 新增 6），覆盖：

- `opt-in persistent queue survives reload and redelivers the stable event id`：首个客户端入队但不 flush/不 destroy（模拟硬卸载），同一存储上构造第二个客户端，重水化后投递且 `event_id` 与首次 emission 完全一致，成功后快照清空。
- `queue persistence is opt-in and writes no queue key by default`：未开启时从不写 `:queue` 键。
- `rehydration prunes expired and malformed snapshot entries`：过期（超 `maxAge`）、畸形（守卫拒绝）条目被裁剪，仅有效条目重水化并投递，投递后快照为空。
- `rehydrated entries awaiting configuration are resolved before delivery`：无 `json` 条目经当前配置补全后按 `schema_version=1/release_revision=1` 投递。
- `a corrupt snapshot fails soft to an empty in-memory queue`：非法 JSON 不抛错，后续 emission/flush 正常。
- `a failing storage port fails soft and keeps in-memory delivery working`：存储全程抛错时仍正常入队投递。

### 变异红绿（sha256 逐字节还原，基线 `/tmp/evia-check/sdk-persist-queue/SHA256SUMS`）

1. 令 `persistQueueState()` 的实际 `setItem` 空转（保留守卫与类型）→ 重载存活测试与依赖快照的裁剪/再解析用例变红（61 pass / 2 fail），证明持久写是存活的必要条件。
2. 删除 `rehydrateQueue()` 的 `maxAge` 过期裁剪 → 裁剪测试精确变红（62 pass / 1 fail），证明过期守卫有效。

每轮用精确字符串替换还原，`sha256sum -c` 全部 OK 后复跑 63 项全绿；prettier 规范化后刷新基线并再次构建/测试通过。

### 门禁

- `make check PYTHON=…/venv/bin/python` 一次通过（EXIT=0）：命名、契约、边界、监控、Go `-race` 全量包、prettier、typecheck、test:client 49、**test:sdk 63**、生产构建全绿。
- `tsc -p tsconfig.json` 构建通过；`git diff` 复核 client.ts 唯一删除块是被重构的内联配置解析，无 prettier 无关重排。

## 明确未做（边界）

- 持久化保证限定在存储可用时的刷新/同上下文导航；浏览器主动回收存储、隐私模式配额淘汰不在 SDK 可控范围。
- 不实现跨标签页写协调：多个标签并发写同一快照不在 v1 范围，正确性由同一 event_id 的幂等重投与服务端去重保证，单上下文刷新路径完整闭环。
- 跨文档会话连续性（session/tab/navigation 跨硬导航延续）仍是独立缺口，本切片不改变现有「每次加载重建 tab/session」语义。
- 未发布 npm；离线队列不改变逐项回执、有界重试与 `flush()` 有界回合的既有语义。
