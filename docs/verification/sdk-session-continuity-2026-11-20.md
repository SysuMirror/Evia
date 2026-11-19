# 浏览器 SDK 跨文档会话连续性验证记录（2026-11-20）

## 问题（确定性缺口）

浏览器 SDK 此前的会话范围（`session_id`、`tab_id`、`sequence`）只存在于内存：每次硬导航或刷新都会重建整个范围，只有匿名 UUID 经 `localStorage` 持久化。`docs/browser-sdk.md` 与功能覆盖清单均标注「刷新/跨文档会重新创建 tab/order/session/page 范围」。本切片以 opt-in 方式把会话范围镜像到按标签页隔离的存储端口，使同一标签页内的硬刷新/跨文档导航延续同一会话，不新增依赖、不改默认行为。

## 范围与语义决策

1. **opt-in、默认关闭**：新增 `ClientOptions.persistSession?: boolean` 与 `sessionStorage?: StoragePort | null`。省略时不读写任何 `:session` 键，历史逐文档范围逐字节不变（原 63 个测试零改动通过即证据）。
2. **端口必须是标签页作用域**：默认绑定浏览器 `sessionStorage`。若两个活标签页共享一个会话，会在同一 session 内发出碰撞的 `sequence`，破坏旅程/漏斗的因果排序，因此刻意不用（跨标签页共享的）`localStorage`；新标签页（含浏览器复制标签页时拷贝的存储副本）获得独立范围，作为与共享匿名标识同类的已记录边界。
3. **快照格式 `evia.session.v1`**：镜像 `session_id`、`tab_id`、`sequence`、`last_activity` 四个可变字段，键 `evia:project:env:writeKey:session`，与匿名 ID 键、持久队列键分离。构造期原样恢复；畸形/外来快照 fail-soft 回退全新范围并覆写。
4. **空闲轮转保持惰性**：恢复不做构造期轮转、不产生任何事件；超时检测仍由 `touchSession()` 在下一次活动时执行，因此长间隔后首次活动观察到的 `sys.session_end`（`idle`）与内存路径完全一致（旧会话上下文发出、随后轮转、外层事件用新会话）。
5. **序号跨文档严格单调**：reload、空闲轮转、`reset()` 都不重置序号；恢复到 `Number.MAX_SAFE_INTEGER` 时下一次 emit 继续 `SEQUENCE_EXHAUSTED` fail-closed。
6. **页面范围刻意不持久化**：刷新即新文档，`page_view_id` 全新（`autoPageViews` 下产生新的保留页面事件），落在延续的会话内。
7. **`reset()` 丢弃持久范围**：轮转后的会话写回快照，下一文档从新会话开始，不会重新加入被丢弃的会话。
8. **构造期 fail-closed、运行期 fail-soft**：开启 `persistSession` 但无可用端口（如 SSR 下传 `null`）时构造抛 `SESSION_STORAGE_UNAVAILABLE`；运行期 `getItem/setItem` 抛错按持久队列同款软降级回内存。
9. **与持久队列正交**：重水化的队列事件保留原会话上下文（不可变历史），重载后新事件在延续会话内以严格更大序号发出。

## 改动清单

| 文件 | 说明 |
| --- | --- |
| `packages/browser-sdk/src/types.ts` | `ClientOptions` 增加 `persistSession` 与 `sessionStorage` 及语义注释 |
| `packages/browser-sdk/src/client.ts` | `PersistedSession` 快照与结构守卫、`sessionStorageDefault()`、`restoreSessionScope()`/`persistSessionState()`；构造器恢复并写回、`touchSession()`/`enqueue`（序号递增后）/`reset()`/`destroy()` 接入写穿 |
| `packages/browser-sdk/test/client.test.ts` | 新增 10 个测试 |
| `tests/browser/sdk-session-continuity.spec.ts` | 全栈浏览器验证（`EVIA_E2E_FULL_STACK=1` 门控） |
| `docs/browser-sdk.md` | 新章节「Cross-document session continuity (opt-in)」与验证边界补充 |

## 验证

### 单元测试（node:test，注入式 TestClock + 假 fetch + Map StoragePort）

`pnpm test:sdk` 全套 73 项通过（原 63 + 新增 10）：

- `persistSession continues the session scope across a simulated reload`：同端口第二客户端恢复同一 `session_id`/`tab_id`，序号严格 +3 延续，快照与投递事件一致。
- `session persistence is opt-in and writes no session key by default`。
- `a restored idle session rotates lazily exactly like the live path`：恢复的过期会话在首次活动时以旧会话发出 `sys.session_end`（idle）、外层事件用新会话，`tab_id` 不变。
- `reset discards the persisted session scope for the next document`。
- `two tabs stay isolated because their session ports are separate`。
- `corrupt or foreign session snapshots fail soft to a fresh scope`（非法 JSON/错误 format/非 UUID/负序号）。
- `persistSession without a usable session port fails closed at construction`（`SESSION_STORAGE_UNAVAILABLE`）。
- `session storage failures degrade softly and never break collection`。
- `rehydrated queue events keep their original session while new events continue it`（与 `persistQueue` 组合，旧事件原上下文、新事件序号 2 延续）。
- `a persisted exhausted sequence keeps failing closed after restore`。

### 全栈浏览器验证（Playwright + 真实 serve/ingest/query + PostgreSQL/Kafka/ClickHouse/Redis）

`EVIA_E2E_FULL_STACK=1 EVIA_E2E_API=http://127.0.0.1:18090 pnpm exec playwright test tests/browser/sdk-session-continuity.spec.ts` 通过（约 12 秒）。链路：UI 登录 → API 建项目/事件定义/草稿/校验/发布（幂等键）/就绪轮询/激活 → 建浏览器来源（合成站 Origin 白名单）→ 本地 HTTP 服务分发 `packages/browser-sdk/dist` 产物 → 隔离浏览器上下文加载合成站：

- 第一文档：`persistSession: true` 客户端 init + emit + flush，`accepted`；读取镜像快照。
- `page.reload()` 硬刷新后第二文档：新客户端恢复同一 `session_id`/`tab_id`，序号严格更大。
- 同上下文第二个标签页：独立 `sessionStorage`，断言 `session_id` 不同（真实浏览器多标签隔离）。
- 仓库读回：`events` 查询按属性 marker 过滤，返回两条事件，`session_id` 均为延续会话、`sequence` 严格递增、`tab_id` 一致 —— 只有该真实查询构成可见性证据。

环境备注：本机 8080 端口被无关进程占用，API 以 `EVIA_LISTEN=127.0.0.1:18090` 运行，控制台经 gitignored `vite.config.local.mjs` 代理；采集端点经 `EVIA_E2E_API` 覆盖，committed 契约默认仍为 8080。另因本机 ClickHouse 命名空间停在迁移 005，先经 `browser_runtime.py prepare` 补齐 006–009 后 ingest worker 恢复（与本切片代码无关的环境修复）。

### 人工核对截图（无凭据）

- 合成站刷新前/刷新后：`session_id`/`tab_id` 不变、序号 1→2（`/data00/home/yangruitao/evi-test/screenshots/sdk-session-before-reload.png`、`sdk-session-after-reload.png`，本地工件不进仓库）。
- 控制台「事件明细」按 `会话标识 = <延续会话>` 过滤的查询结果：同一会话跨刷新两条事件（`console-events-continued-session.png`，同上）。
- 仓库层直查复核：`event_facts` 中该 `session_id` 恰两行、`tab_id` 一致、序号 1/2、时间间隔约 0.5 秒（跨刷新）。

## 边界（不声称）

- 复制标签页会拷贝 `sessionStorage`，两个活标签页随后各自写穿同一快照并可能发出碰撞序号；这是与匿名标识共享同级的已记录边界，v1 不做跨标签页协调。
- 会话延续依赖 `sessionStorage` 可用；存储被浏览器清空时该次连续性丢失（软降级），不回溯合并。
- `identify()` 的 uid 不持久化：登录态延续仍由业务方在每页调用 `identify`，身份图/跨身份合并不在本切片范围。
- Node 单测与真实浏览器证据不构成百万用户容量或生产 HA 声明。
