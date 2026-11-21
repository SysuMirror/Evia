# 会话回放首个全链验证（2026-11-22）

## 范围与边界

本切片复用平台通用 `sys.content_chunk`，平台不增加 newSSE、DOM 或回放业务字段。业务侧用：

- `content_type=session_replay_snapshot|session_replay_mutations`
- `stream_id=浏览器标签页内稳定 replay UUID`
- `item_id=每个加密段 UUID`
- `actor_type=browser`

交付了真实浏览器采集、压缩分段、浏览器端混合加密、opaque 仓库、content-worker 解密、安全索引、授权读取及沙箱重建证据；后续同日补齐正式 Evia 控制台的会话流/内容项发现与安全 DOM 快照预览。mutation 逐帧精确播放仍未交付。

## 采集与数据量控制

newSSE `src/analytics/replay.ts`：

- 按环境配置采样，采样决定与 replay stream 固定在 `sessionStorage`；默认关闭。
- 每条会话默认 2 MiB 压缩后预算，可配置 64 KiB–8 MiB；累计字节写入 `sessionStorage`，硬刷新后继续计费；超过预算立即停止，不明文降级。
- DOM snapshot 最多 4 Mi 字符；MutationObserver 记录 child/text/有限属性；每 3 秒或 100 条 flush。
- scroll 最多每 250 ms 一条；click 用归一坐标和 bounded structural path；resize/navigation 单独记录。
- payload 用浏览器 `CompressionStream(gzip)`，base64url 后按 16,000 字符分段，再逐 item 走 `emitEncryptedContentChunk`。
- 所有 payload 串行送入预算/加密队列，避免并行 flush 绕过预算。

## 隐私与安全

- 移除 `script/noscript/iframe/object/embed`。
- 移除 `on*`、nonce、value、srcdoc、含 token 的属性；URL 丢弃 query/hash。
- `input/textarea/select/contenteditable/[data-evia-mask]/.evia-mask` 内容统一替换为 `••••`。
- 测试向 DOM 注入密码和 `data-evia-mask` 私密文本，授权解密并重组全部 payload 后逐字检查，两段测试原文均不存在。
- 浏览器请求仅含固定 600-byte padded AES-GCM ciphertext 和 RSA-OAEP-256 wrapped key；telemetry token 存在，未配置/失败时不发送明文。
- 回放渲染在无脚本 sandbox iframe，并附收紧 CSP；验证工具不执行捕获页面脚本。

## 真实 E2E 结果

流 `366e7601-60ce-4171-8e94-c7c484a509d7`：

1. 真实 newSSE 页面产生 snapshot、DOM child/attribute、navigation、click、resize、scroll 记录。
2. 浏览器捕获 8 个 collect req/res 批次，均 HTTP 202；逐事件回执 accepted。代表请求：`sys.content_chunk`、`content_type=session_replay_snapshot`、`actor_type=browser`、AAD v1、fixed-600-v1；请求 properties 无 `content`。
3. ClickHouse 审计：
   - snapshot：108 块；mutations：20 块。
   - 所有 ciphertext 长度均 822；`JSONHas(properties_json,'content')=0`。
4. content-worker：PostgreSQL 索引 4 个 snapshot item / 108 chunks、2 个 mutation item / 20 chunks；0600 artifact 经 hash 校验重组。
5. 同一 snapshot item：未认证读取 401 `SESSION_REQUIRED`；项目 owner 授权读取 200，返回 `content_type/stream/item/actor/content_length=16159/truncated=false`，内容 SHA-256 `88a0780b142aa4587bd9eff92c56a7089158c3b3ae704ecd710da462b415c8c4`。
6. 验证播放器解压并重组 4 个 payload group、62 条 mutation（children 56、attribute 1、navigation 2、click/resize/scroll 各 1），成功重建登录页；mask 原文检查通过。
7. 负向浏览器守卫：固定 `sampled=0` 时 replay stream/item/collect 均不存在；预置 `used_bytes=2 MiB` 时 replay item/collect 为 0，证明采样关闭和跨刷新预算耗尽均在 DOM observer/发送前终止。

截图：

- `screenshots/newsse-replay-recording.png`
- `screenshots/newsse-replay-network-req-res.png`
- `screenshots/evia-replay-authorized-api.png`
- `screenshots/evia-session-replay-player.png`
- `screenshots/evia-formal-session-replay-console.png`
- `screenshots/evia-formal-session-replay-preview.png`

## 正式控制台工作区

- 通用发现 API：`GET .../content/streams` 与 `GET .../content/streams/{stream_id}/items`，要求 environment、1–8 个规范化 content types、limit/cursor；既支持 owner session，也支持环境绑定的 `content.read` 机器凭据。
- PostgreSQL 仅从完整且 chunk_count 一致的 item 聚合；迁移 047 增 stream/time 索引。流按 `(last_received_at DESC,stream_id)`，item 按 `(received_at,item_id)`；签名游标绑定 actor/project/environment/stream/type，并用 `created_at <= as_of` 冻结分页视图，防止 worker 并发追加导致跨页漂移。
- owner 项目权限投影新增 `content.read`；editor/viewer 仍由存储事务内 owner 检查拒绝。机器授权 UI 同步公开该独立敏感权限。
- 控制台「会话回放」工作区提供流列表、完整内容项列表、分页、错误/空态和安全快照预览。快照读取不进入 React Query 缓存；最多 600 个 item、8 并发读取、16 MiB 解压上限。
- 预览前再次移除 script/iframe/object/embed/base/link、事件处理器、外部 src/href/action 与凭据属性，注入 `default-src 'none'` CSP，并在不含任何 sandbox capability 的 iframe 中使用 `no-referrer` 展示。
- 真实全栈 UI：4 条 replay stream、所选流 6 个完整 item；stream/items/detail 请求全部 200，正式工作区重组 4 个 snapshot item 并显示「欢迎回来」登录页。全栈 Playwright spec 1/1、replay/permission model 5/5 通过。

## 修复的升级缺陷

原始敏感消息迁移在已升级数据库中残留 `actor_type IN ('user','assistant')` 和状态枚举，违反通用平台模型并阻断 `actor_type=browser`。迁移 046 删除产品专属约束，改为 actor/status bounded string；新建库仍由当前 044 的通用定义建立约束。

验证过程中旧二进制未嵌入迁移 046，因此先对隔离验证库等价执行 SQL 完成 E2E；源迁移随后经 044→045→046→047 隔离 PostgreSQL 冷启动并用 `actor_type=browser,status=captured` 实插验证。完整 `make backend-test`（gofmt、go vet、`go test -race ./...`）与 web gate（Prettier、typecheck、API client 52/52、SDK 79/79、SDK/console build）通过；content discovery 真实 PostgreSQL `-race` 集成与 OpenAPI/JSON Schema 全契约验证通过。

## 尚未完成

- mutation 精确重放（正式工作区当前展示最新完整 snapshot，不逐帧应用 mutation）。
- 回放访问审计 UX、云对象存储适配器、生产 KMS/HSM gateway 实部署。
- SPA 长会话的更完整节点 ID 模型、iframe/Shadow DOM 策略、性能与丢帧基准。
