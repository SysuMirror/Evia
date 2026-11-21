# 会话回放首个全链验证（2026-11-22）

## 范围与边界

本切片复用平台通用 `sys.content_chunk`，平台不增加 newSSE、DOM 或回放业务字段。业务侧用：

- `content_type=session_replay_snapshot|session_replay_mutations`
- `stream_id=浏览器标签页内稳定 replay UUID`
- `item_id=每个加密段 UUID`
- `actor_type=browser`

交付了真实浏览器采集、压缩分段、浏览器端混合加密、opaque 仓库、content-worker 解密、安全索引、授权读取及沙箱重建证据。正式 Evia 控制台中的回放发现列表和播放器仍未交付；当前播放器是验证工具，不代表产品工作区完成。

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

## 修复的升级缺陷

原始敏感消息迁移在已升级数据库中残留 `actor_type IN ('user','assistant')` 和状态枚举，违反通用平台模型并阻断 `actor_type=browser`。迁移 046 删除产品专属约束，改为 actor/status bounded string；新建库仍由当前 044 的通用定义建立约束。

验证过程中旧二进制未嵌入迁移 046，因此对隔离验证库等价执行 SQL 后完成 E2E；源迁移已加入仓库。后端 content/storage/control focused tests通过。一次全量重编译尝试因本机访问 `proxy.golang.org` 超时而失败，非编译错误；后续需在依赖缓存/网络恢复后执行完整 `make backend-test` 与迁移冷启动验证。

## 尚未完成

- Evia 控制台中的 replay stream 发现/列表、产品播放器和审计 UX。
- 云对象存储适配器、生产 KMS/HSM gateway 实部署。
- mutation 精确重放（当前验证播放器展示 snapshot 与记录时间线；未逐帧应用所有 mutation）。
- SPA 长会话的更完整节点 ID 模型、iframe/Shadow DOM 策略、性能与丢帧基准。
