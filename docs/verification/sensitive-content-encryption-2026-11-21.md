# 敏感内容字段级加密平台验证（2026-11-21）

## 链路

newSSE 浏览器内容加密器 → AES-256-GCM 分片（≤600 bytes/块）→ RSA-OAEP-256 包装消息 key → `/v1/collect` → Kafka/ClickHouse（opaque ciphertext）→ `evia content-worker`（PostgreSQL generation lease/cursor）→ local JWK 或 HTTPS KMS `KeyUnwrapper` → GCM AAD/标签认证 → 0700 根目录/0600 原子 chunk 对象 → 迁移 044 PostgreSQL 索引 → owner / 机器 `content.read` 授权 API 组装。

## 真实证据

- `/v1/config` 200 返回 `content_encryption{key_id,alg,public_jwk}`，public JWK 含 `kty/n/e/key_ops=[encrypt]` 且无 private `d`；配置带 Ed25519 `config_signature{key_id,digest_sha256,signature}`。SDK trust anchor 验证通过；拦截响应替换 content key 后 `INVALID_CONFIG_SIGNATURE` 且 collect=0。
- 有效浏览器 collect 携带 368 字符短期 `X-Evia-Telemetry` token 并 202；篡改或缺失 token 均 401 `TELEMETRY_TOKEN_INVALID`。
- newSSE 将小秋业务映射到平台通用 `sys.content_chunk{content_type=ai_chat_message,stream_id,item_id,actor_type}`；1248-byte 中文合成对话分 3 块，浏览器 req 无明文，三块响应均 202/accepted；三块 ciphertext 均固定 822 字符（600-byte padded plaintext + GCM tag），测试私钥按 scope-bound AAD 解密/去 padding 后与原文逐字节一致。
- ClickHouse 最新 v1 三块 ciphertext 固定长 822/822/822，`JSONHas(properties_json,'content')=0`。
- content-worker 处理通用 `sys.content_chunk` 并写 0600 `.chunk` 工件，PostgreSQL 以 content_type/stream/item/actor 索引完整内容项；`ai_chat_message` 最新 item 三块完整入库，cursor/lease 提交后无重复。
- owner `GET .../content/items/{item_id}?environment=development` 返回 200，内容 1248 bytes、`truncated=false`、与原文一致；无会话返回 401 `SESSION_REQUIRED`。
- unit：local JWK decrypt、AAD 篡改拒绝、vendor-neutral KMS unwrap（Bearer 0600 token、loopback test endpoint）、文件原子往返/路径拒绝、worker 成功推进与解密错误不推进。

## 门禁

- `make backend-test`（`-race ./...`）EXIT=0；content/access/contract/delivery/transport/config focused suites全绿。
- `make web-check` EXIT=0；SDK 79/79 + typecheck/build。
- OpenAPI/JSON Schema 验证全绿。

## 部署边界

测试环境使用 0600 private JWK 与文件对象存储。生产应选择 HTTPS KMS gateway/HSM 适配器与云对象存储适配器；仓库提供 vendor-neutral KMS unwrap 合约与 `ArtifactStore` 端口，不绑定厂商 SDK。
