# 敏感内容字段级加密方案与交付边界

> **状态：平台闭环已交付（2026-11-21）。** 浏览器混合加密、`/v1/config` 公钥下发、content-worker 解包/验签/解密、0600 文件对象存储、PostgreSQL 安全索引、owner / `content.read` 授权读取与 KMS 适配器均已实现和验证，见[验证记录](../verification/sensitive-content-encryption-2026-11-21.md)。云厂商特定 KMS 网关、云对象存储适配仍由部署方提供。

## 目标与边界

对小秋对话原文、搜索原文等高敏内容做应用层加密，使 collector、Kafka、ClickHouse 与普通日志链路不出现明文。普通访问引用（route/post_id/product_id）、长度、角色、状态、时延继续明文供分析。Base64 只是编码，不提供保密性。生产仍必须 HTTPS，字段级加密不替代 TLS。

## 浏览器 envelope

平台保留事件 `sys.content_chunk` 每块通用字段：

- 索引：`content_type`、`stream_id`、`item_id`、`actor_type`、`chunk_index`、`chunk_count`
- 密文：`ciphertext`、`nonce`、`wrapped_key`、`key_id`、`alg=A256GCM+RSA-OAEP-256`
- 安全元数据：`content_length`、`truncated`，可选 `response_status`、`duration_ms`

生产方为每个内容项生成随机 AES-256-GCM key，最多提交 32 块，并显式选择固定块模式：`fixed-600-v1` 保持既有 600-byte 块（2-byte 长度 + 最多 598-byte payload），用于对话和旧接入兼容；`fixed-4096-v2` 使用 4096-byte 块（2-byte 长度 + 最多 4094-byte payload），供回放等高吞吐通用内容减少事件数。每块使用随机 96-bit nonce 与 AAD（project/environment/key/stream/item/actor/chunk/count/length/status）独立认证。AES key 经平台 RSA-OAEP-256 公钥包装，私钥永不进入浏览器；加密不可用/失败时 fail-closed，不降级明文。平台不按 `content_type` 推断模式，业务生产方自行映射内容语义。

## 平台实现

1. `/v1/config` 可选 `content_encryption`：`key_id`、`alg=RSA-OAEP-256`、public JWK；参与 ETag 哈希，来源授权与 key 轮换配置由 serve 读取。配置另带 Ed25519 detached `config_signature`（签 canonical SHA-256 digest）；SDK 通过构建期 trust anchor 验签并拒绝 unsigned/替换公钥/私钥参数。SDK 支持 206 事件上限（200 项目 + 6 `sys.*`）。
2. `evia content-worker` 通过 ClickHouse 游标读取 `sys.content_chunk`，持有 PostgreSQL generation lease；密文解包/认证任一步失败即不推进游标。AAD v1 绑定 project/environment/key/stream/item/actor/chunk/count/content_length/truncated/status/duration；解密器按闭集 padding 标记要求 opened plaintext **恰好**为 600 或 4096 bytes，再校验内嵌 2-byte payload 长度位于 `1..block-2`。未知标记、600/4096 降级或错配、AAD 篡改均 fail-closed。旧开发块只在空版本标记时走受限的 ≤600-byte 兼容分支，不猜测 4096 模式。
3. `KeyUnwrapper` 是 KMS/HSM 端口：
   - 生产 `KMS` 适配器只向 HTTPS（本机开发可显式放行 loopback HTTP）发送 wrapped key + key_id，Bearer token 从 0600 文件读取；返回 32-byte AES key，不持久化、不记录日志。
   - 开发/单节点提供 0600 private JWK 适配器；private 参数从不进入 API/SDK。
4. 解密块写入 0700 根目录、0600 原子文件（sha256 校验，路径由作用域/message/chunk/event 哈希生成，禁止路径逃逸）；PostgreSQL 迁移 044 只保存对象 key、hash、会话/消息/角色/分片/长度/状态与 durable cursor。
5. `GET /api/v1/projects/{project_id}/content/items/{item_id}?environment=...` 按 chunk 索引组装并逐块校验 sha256。人类仅 owner 可读；机器需专用 `content.read`，未认证 401、非 owner/无 grant fail-closed。每次已授权尝试通过通用项目审计写入 `content.item.read`、主体/凭据归属、项目/环境、安全资源键、时间与 success/failure；不写明文、密文、对象 key、密钥或凭据内容，成功审计不能提交时不返回内容。
6. 浏览器采集请求带 5 分钟窗口轮换的 HMAC `X-Evia-Telemetry` token，绑定 source/project/environment/origin/config revision/write_key hash/expiry；collector 在来源解析后复核 scope 且要求事件 revision 一致。缺失/篡改/过期 token 均 401，CORS 只额外允许该固定 header。

## 验证证据

- 浏览器：1248-byte 中文正文分 3 块，经真实 `/v1/collect` 202 + 逐事件 accepted；请求体搜索不到原文，本机测试私钥按 AAD 解密后与原文逐字节一致。
- ClickHouse：只存 ciphertext（822/822/86 字符），`JSONHas(properties_json,'content')=0`。
- content-worker：扫描后写 9 个 0600 chunk 工件；PostgreSQL 索引三条完整消息；授权 API 返回 200，组装内容与原文一致（1248 bytes、truncated=false）；未认证请求返回 `SESSION_REQUIRED` 401。
- 单测：local JWK decrypt + AAD 篡改拒绝、KMS HTTPS/认证 unwrap 适配器、文件 store 原子往返/路径拒绝、worker 成功推进与解密失败不推进；SDK 77/77（含 public config 公钥正负校验）。

## 部署边界

- 云厂商 KMS/HSM 需部署一个符合 unwrap 合约的网关或实现 `KeyUnwrapper`；仓库提供 vendor-neutral HTTPS 适配器，不绑定厂商 SDK。
- 文件对象存储是私有部署 v1；S3/OSS 适配需实现同一 `ArtifactStore`（Put/Read）并保持对象端服务加密与访问审计。
- 搜索 TopN 的不可逆 fingerprint 尚未由 content-worker 计算；当前搜索原文按项目策略可明文 string 或复用 encrypted chunk。
