# 采集器 sys.* 系统目录旁路（切片 2）验证记录（2026-11-15）

## 背景与范围

切片 1 已把五个保留 `sys.*` 事件的平台契约 embed 进二进制并提供 `SystemCatalog` 加载器，但
采集器仍只查项目发布计划，点号键必然在规则匹配处被拒。本切片在采集器接入系统目录分支，
**不触碰** SDK 发送门控、公开配置、Kafka/ClickHouse 落库（切片 3），也不改任何契约与迁移。

## 改动清单

| 文件 | 说明 |
| --- | --- |
| `internal/ingestion/collector.go` | `Collector.systemCatalog` 字段与 `WithSystemCatalog` option；item 循环的系统分支；准入复核对系统记录的显式旁路 |
| `cmd/evia/runtime.go` | 装配时 `LoadSystemCatalog()`，失败即 `stageError` 拒绝启动 |
| `internal/ingestion/collector_system_test.go` | 7 组采集器级测试 |

## 关键设计

1. **opt-in 且默认逐字节不变**：未调用 `WithSystemCatalog` 时系统分支整体短路，`sys.*` 仍走
   项目计划并以 `SCHEMA_NOT_PUBLISHED` 拒绝（专门测试钉住）；既有全部采集器测试零修改通过。
2. **校验强度与普通事件一致**：系统事件仍先过共享信封契约（未知上下文字段在分支前即
   `INVALID_EVENT`），再依次做规则存在性+`schema_version` 匹配（`SCHEMA_NOT_PUBLISHED`）、
   来源 kind 闭集（五规则 browser-only，违例 `SOURCE_KIND_FORBIDDEN`）、属性 JSON Schema
   （`PROPERTY_SCHEMA_MISMATCH`，含 `additionalProperties:false`）、context 解析
   （`INVALID_EVENT_CONTEXT`）；批次预算、来源认证、队列发布与逐项回执路径完全复用。
3. **不查项目计划、不占计划缓存**：系统分支不触碰 `batchPlans`/`planErrors` 与单批次 8 次
   plan-miss 放大护栏；`TestSystemEventsBypassReleaseLeaseRecheck` 让 plan 解析器直接报错，
   系统事件仍 202，证明无依赖。
4. **租约准入旁路**：系统记录以 `systemItems` 按位置标记，最终准入复核跳过
   `admittedRevisions` 判定；平台契约即发布，运行中的二进制就是资格来源。
5. **`release_revision` 口径**：原样保留信封值仅作溯源（pipeline `record.go` 与 CH 均要求
   ≥1/非空），不引入哨兵值、不放宽既有不变量；详见设计文档同节。

## 验证

### 单元测试（`internal/ingestion`，`-race`）

- `TestSystemEventAcceptedWithoutProjectPlan`：`sys.page_view`（空属性）与 `sys.page_scroll`
  （完整示例属性）双事件 202 入库，plan 解析调用 0 次，记录的键/版本/修订/来源/匿名身份与
  批次预算计数正确；
- `TestSystemEventRejections` 五个子测：未知保留键、错误 schema 版本 →
  `SCHEMA_NOT_PUBLISHED(schema_version)`；枚举越界与额外属性 →
  `PROPERTY_SCHEMA_MISMATCH(properties)`；server 来源 → `SOURCE_KIND_FORBIDDEN`；全部
  非 retryable、零发布、零 plan 调用；
- `TestSystemEventStillPassesEnvelopeGate`：系统分支不削弱信封门控；
- `TestMixedBatchKeepsProjectAndSystemPathsSeparate`：普通事件与系统事件混批各自只走对应
  路径，plan 恰好解析 1 次，记录顺序保持；
- `TestSystemEventsBypassReleaseLeaseRecheck`：plan 存储故障不影响系统事件；
- `TestSystemBranchIsOptIn`：未装配目录时回退原拒绝路径且仍查一次项目计划。

### 变异红绿取证

1. 删除准入复核中的 `systemItems[j]` 旁路 → 系统记录落入租约判定，
   `TestSystemEventAcceptedWithoutProjectPlan` 如期变红（`EVENT_PLAN_NOT_READY`），混批测试
   同步变红；
2. 把系统分支来源 kind 强制改为恒放行 →
   `TestSystemEventRejections/server_source_forbidden_for_browser-only_rule` 如期变红
   （误返回 202）。

两次变异均以 sha256 校验逐字节还原，还原后 `internal/ingestion` 与 `cmd/...` 全部 `-race`
转绿，`gofmt`/`go vet` 干净。

### 门禁

完整 `make check` 退出码 0；命名检查无禁词。

## 明确未做（切片边界）

- SDK 的 `/v1/config` 公开配置仍不含 `sys.*`，客户端侧发布门控继续把系统事件结算为
  `EVENT_NOT_PUBLISHED` 而不发送；本切片只保证**到达采集器的**合规系统信封能被接收。
- Kafka→ClickHouse 端到端落库、可查询验证与文档总核销属切片 3。
