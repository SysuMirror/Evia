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
- 每条会话默认 2 MiB gzip+base64url 编码字符预算，可配置 64 KiB–8 MiB；累计字符数写入 `sessionStorage`，硬刷新后继续计费；报告必须同时区分 encoded chars 与实际 gzip bytes。超过预算立即停止，不明文降级。
- DOM snapshot 最多 4 Mi 字符；MutationObserver 记录 child/text/有限属性；每 3 秒或 100 条 flush。
- scroll 最多每 250 ms 一条；click 用归一坐标和 bounded structural path；resize/navigation 单独记录。
- payload 用浏览器 `CompressionStream(gzip)`，base64url 后按 10,000 字符分段，使固定 padding 后的 encrypted chunks 落在 SDK 单个 20-event batch 内，再逐 item 走 `emitEncryptedContentChunk` 并等待 accepted。
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

### 已登录真实业务旅程

使用测试账号经真实登录 UI（凭据不进入日志/截图/提交），依次运行首页、点击帖子详情、顶部搜索“大创”、热榜、分区、评分、商城、音乐、消息、小秋、简历、投票，共 13 个旅程节点；未发布评论、私信、商品或投票，也未触发其他业务写入。

- 产生 login/page/post/search/hot/rating/shop/message/chat 与 sys.page_view/identity_link/heatmap/content 共 15 类事件；最终 78 批 collect 全部 202，无 page error。
- 深链搜索缺少 search_id 时原实现发送 `unknown`，真实 collector 以 `PROPERTY_SCHEMA_MISMATCH` 422 拒绝；修复为生成 UUID，并用真实顶部搜索复验 `search_performed` + `search_results_viewed` 全 202。
- 高体积 DOM 暴露了 SDK queue backpressure 边界：原 helper 在同步入队失败时仍返回 true。修复为每个 10,000 字符 segment 控制在单个 20-event batch 内，显式 flush、队列满先 flush 重试，并等待全部 emission outcome=accepted 后才记 item。
- 最终样式流 `06997b4e-e594-47f7-8f6c-64dda21e7a39`：浏览器记 62 items；ClickHouse 与 PostgreSQL 同为 62 个 distinct/完整 items（snapshot 41 + mutations 21），三方严格相等；892 个密文块全 822 字符，CH `content` 字段计数 0。
- snapshot 延迟至路由稳定后 2 秒，内联最多 1 MiB CSSOM，并只额外保留 Vue scope 标记；最终正式 Evia sandbox 可恢复已登录首页布局，组装 13 个 snapshot segment，页面正文长度 2489。
- 移动端仿真使用 Chromium `390×844`、deviceScaleFactor=3、isMobile/touch，真实登录后覆盖首页、顶部搜索、热榜、商城、消息和小秋；63 批 collect 全 202、13 类事件、page error=0。流 `763f9135-45ca-4a84-ac52-02577cbfa240` 在 ClickHouse/PostgreSQL 为 60 个完整 items / 846 chunks、CH 明文字段0；正式 v2 工作区按 viewport=390×844 重建移动页面并加载 6 条连续记录。

截图：

- `screenshots/newsse-replay-recording.png`
- `screenshots/newsse-replay-network-req-res.png`
- `screenshots/evia-replay-authorized-api.png`
- `screenshots/evia-session-replay-player.png`
- `screenshots/evia-formal-session-replay-console.png`
- `screenshots/evia-formal-session-replay-preview.png`
- `screenshots/newsse-auth-home.png`
- `screenshots/newsse-auth-post-detail.png`
- `screenshots/newsse-auth-search.png`
- `screenshots/newsse-auth-heat.png`
- `screenshots/newsse-auth-shop.png`
- `screenshots/evia-authenticated-session-replay-console.png`
- `screenshots/evia-authenticated-session-replay-preview.png`
- `screenshots/newsse-mobile-home.png`
- `screenshots/newsse-mobile-search.png`
- `screenshots/newsse-mobile-heat.png`
- `screenshots/evia-click-heatmap-mobile.png`

## 正式控制台工作区

- 通用发现 API：`GET .../content/streams` 与 `GET .../content/streams/{stream_id}/items`，要求 environment、1–8 个规范化 content types、limit/cursor；既支持 owner session，也支持环境绑定的 `content.read` 机器凭据。
- PostgreSQL 仅从完整且 chunk_count 一致的 item 聚合；迁移 047 增 stream/time 索引。流按 `(last_received_at DESC,stream_id)`，item 按 `(received_at,item_id)`；签名游标绑定 actor/project/environment/stream/type，并用 `created_at <= as_of` 冻结分页视图，防止 worker 并发追加导致跨页漂移。
- owner 项目权限投影新增 `content.read`；editor/viewer 仍由存储事务内 owner 检查拒绝。机器授权 UI 同步公开该独立敏感权限。
- 控制台「会话回放」工作区提供流列表、完整内容项列表、分页、错误/空态和安全快照预览。快照读取不进入 React Query 缓存；最多 600 个 item、8 并发读取、16 MiB 解压上限。
- 预览前再次移除 script/iframe/object/embed/base/link、事件处理器、外部 src/href/action 与凭据属性，注入 `default-src 'none'` CSP，并在不含任何 sandbox capability 的 iframe 中使用 `no-referrer` 展示。
- 真实全栈 UI 基线：4 条 replay stream、所选流 6 个完整 item；stream/items/detail 请求全部 200，正式工作区重组 4 个 snapshot item 并显示登录页。追加已登录旅程后工作区列出 11 条流，最新短旅程 22 个完整 item，重组 13 个 snapshot segments 并恢复有 scoped 样式的登录后首页。全栈 Playwright spec 1/1、replay/permission model 7/7 通过。

## v2 精确 mutation 播放

在不改变通用 `sys.content_chunk`、加密、worker、存储和发现 API 的前提下，新增向后兼容内层 v2：

- 每次文档加载有 `document_id/document_seq`，payload 与 mutation record 分别单调编号；route、capture time 和 payload ID 在入 delivery queue 前封存，网络延迟不再改变顺序语义。
- sanitized snapshot 为 element/text/comment 分配文档内稳定 node ID；mutation 用 node ID 表达 text、attribute set/remove、child remove/add 与 `before_id` 插入锚点，不再依赖会随 DOM 变化的 nth-of-type path。
- snapshot 带 `base_seq`；播放器只应用其后的连续记录，遇到 payload/record 序号缺口、未知节点、错误父节点或冲突 ID 立即停止，不做 best-effort 猜测。v1 历史流继续安全展示 snapshot，但明确标注不可精确播放。
- 控制台读取 snapshot+mutations，在不进入 QueryClient 的局部状态中构建 timeline；提供播放/暂停/回到快照/进度 seek。iframe 仅增加 `allow-same-origin` 供可信父播放器操作 DOM，仍无 scripts/forms/popups/downloads，并保留 no-referrer 与 deny-all CSP。
- 真实 v2 流 `d6d86bfa-2511-4b01-89e7-821e14c0c214`：浏览器、ClickHouse、PostgreSQL 均为 25 items / 335 encrypted chunks；正式播放器识别 v2、构建 117 条连续记录并实际 seek/apply 前 10 条，无 gap/unknown-node 错误。模型测试覆盖 v1、v2 连续 timeline、gap fail-closed 与 segment 冲突；全栈 browser spec 验证授权读取、sandbox capability 和 seek。

明确边界：精确指 sanitized light DOM；Shadow DOM、iframe contents、canvas/WebGL、媒体状态和 CSSOM 后续变化不在 v2 范围。

## 修复的升级缺陷

原始敏感消息迁移在已升级数据库中残留 `actor_type IN ('user','assistant')` 和状态枚举，违反通用平台模型并阻断 `actor_type=browser`。迁移 046 删除产品专属约束，改为 actor/status bounded string；新建库仍由当前 044 的通用定义建立约束。

验证过程中旧二进制未嵌入迁移 046，因此先对隔离验证库等价执行 SQL 完成 E2E；源迁移随后经 044→045→046→047 隔离 PostgreSQL 冷启动并用 `actor_type=browser,status=captured` 实插验证。完整 `make backend-test`（gofmt、go vet、`go test -race ./...`）与 web gate（Prettier、typecheck、API client 52/52、SDK 79/79、SDK/console build）通过；content discovery 真实 PostgreSQL `-race` 集成与 OpenAPI/JSON Schema 全契约验证通过。

## 尚未完成

- 多文档/多 checkpoint 章节切换、播放速度选择与 gap 后 resync 跳转；当前精确播放器聚焦最新完整 v2 snapshot 后的连续 sanitized light-DOM 记录。
- 回放访问审计 UX、云对象存储适配器、生产 KMS/HSM gateway 实部署。
- SPA 长会话的更完整节点 ID 模型、iframe/Shadow DOM 策略、性能与丢帧基准。
