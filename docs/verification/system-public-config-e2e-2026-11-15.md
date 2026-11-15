# 系统事件公开配置与端到端落库（切片 3）验证记录（2026-11-15）

## 背景与范围

切片 1 交付 embed 系统目录、切片 2 让采集器接收 `sys.*` 信封，但 SDK 的 `/v1/config`
公开配置仍只含项目发布事件，客户端发布门控把系统事件结算为 `EVENT_NOT_PUBLISHED` 而不发送。
本切片让公开配置携带五个保留事件，并以真实 Kafka/ClickHouse 验证端到端可查询。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 领域 | `internal/delivery/types.go` | `ReservedEvent`/`ReservedEventProvider` 抽象；双层预算常量（项目 200、保留 5、公开配置 205） |
| 领域 | `internal/delivery/service.go` | 可选 `WithReservedEvents`；`PublicConfig` 追加保留事件，复用来源 kind 白名单与 `publicSchema`，超限 fail-closed |
| 领域 | `internal/delivery/manifest.go` | 字面量 200 改用 `ProjectReleaseEventLimit`，语义不变 |
| 装配 | `cmd/evia/runtime_delivery.go` | 主包适配器把 ingestion `SystemCatalog` 转成 delivery 接口，避免包循环；加载失败拒绝启动 |
| 契约 | `contracts/http/openapi.json` | PublicConfig `maxItems` 200→205 并注明 5 个保留槽 |
| 生成 | `gen/wire/contracts.gen.json`、`api-client/schema.gen.ts` | codegen 同步，无新类型 |
| SDK | `packages/browser-sdk/src/validation.ts` | 配置事件上限 200→205 |
| 测试 | delivery 服务测试、SDK 测试、pipeline 集成测试 | 见下 |

## 关键设计

1. **依赖方向**：delivery 不导入 ingestion。主包（cmd/evia）实现
   `systemEventProvider` 适配器，把 `SystemCatalog.Keys()/Rule()` 转为
   `[]delivery.ReservedEvent`；两包各自独立可测。
2. **opt-in 零回归**：provider 为 nil 时 `PublicConfig` 逐字节保持历史行为，既有全部 delivery
   测试零修改通过；ETag 随配置内容变化，两次相同调用 ETag 稳定。
3. **来源过滤一致**：保留事件携带 `allowed_source_kinds`，与项目事件共用 `allows()` 判定；
   五个规则均 browser-only，server 来源配置不含任何 `sys.*`。
4. **无键冲突**：项目目录键受禁点号 CHECK 约束，保留键全部带 `sys.` 前缀，结构上不可能重叠。
5. **预算显式化**：项目发布仍 ≤200；公开配置总数 ≤205（200 项目 + 5 平台）。OpenAPI、SDK、
   服务端三处同一口径，服务端超限返回 `PUBLIC_CONFIG_LIMIT_EXCEEDED` fail-closed。
6. **私有注解不泄露**：保留 schema 同样经过 `publicSchema`，`title/description/default/examples`
   在公开配置中剥离。

## 验证

### 单元测试

- delivery 新增 4 组：浏览器来源追加保留事件且顺序确定（项目事件在前、provider 序在后）、
  ETag 确定性与无 provider 时的历史形态、server 来源过滤、205 边界与超限守卫；
- browser SDK 新增 2 组：205 事件（含五个保留键）配置可初始化、206 被 `INVALID_CONFIG`
  拒绝；含 `sys.operation_failed` 的配置使事件真正发送，信封 `release_revision` 等于来源当前
  项目发布修订（切片 2 的溯源口径），SDK 全套 57 测通过。

### 变异红绿（三轮，均 sha256 逐字节还原）

1. 移除保留事件来源 kind 过滤 → `TestPublicConfigReservedEventsRespectSourceKind` 变红
   （server-only 保留事件泄漏进浏览器配置）；
2. 移除 205 上限守卫 → `TestPublicConfigReservedEventBudgetGuard/over_limit` 变红；
3. SDK 上限改回 200 → 205 事件配置用例 `INVALID_CONFIG` 变红。

### 真实 Kafka + ClickHouse 端到端

`TestSystemEventsFlowCollectorKafkaClickHouse`（`-tags=integration`，隔离主题与
`evia_pipeline_<suffix>` 库，应用全部 deploy/clickhouse 迁移）：

1. 用真实 `KafkaProducer` 作为采集器 Queue、真实 embed `SystemCatalog`、项目计划解析器
   桩在被调用时直接 `t.Fatal`；
2. 采集器接收 `sys.page_view`（空属性）与 `sys.page_scroll`（完整属性）两个信封，202 全接收；
3. 真实 worker 消费 Kafka 并物化 ClickHouse；
4. 断言 `event_facts_raw` 与 `event_facts` 视图各 2 行，键字典序正确，`schema_version=1`、
   `release_revision=7` 溯源保留，scroll 的 `direction/duration_ms` 可经 JSON 函数查询，
   `source_kind='browser'` 与项目作用域正确；
5. 测试结束自动 DROP DATABASE SYNC 与删除主题，事后核查零残留。

同包核心 `TestKafkaClickHouseDurableReplayAndQuarantine` 同步通过。两个环境门控失败
（`TestReplicatedSinkBaseline` 需 HA 负载均衡变量、`TestReadinessReaderGroupsAndScopesProofs`）
在干净 HEAD（0284fe1）上同样失败，与本切片无关，单节点沙箱不具备其前置环境。

### 门禁

完整 `make check` 退出码 0；codegen 幂等；命名检查无禁词。

## 边界

- 系统事件经浏览器来源的 write_key/Origin 认证与同一预算/队列/回执路径，没有特权旁路；
- 控制台与查询侧对 `sys.*` 键无特殊 UI，作为普通事件键参与即席查询，这是预期行为而非缺口；
- 平台保留事件仍不可经公开 `emit()` 伪造，该不变量由 SDK 既有测试继续守护。
