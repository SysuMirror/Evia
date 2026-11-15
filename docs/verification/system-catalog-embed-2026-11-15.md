# 系统事件目录底座（embed 平台契约）验证记录（2026-11-15）

## 背景

浏览器 SDK 已能产生五个保留 `sys.*` 事件，但服务端无运行时目录承载，采集器按来源项目目录
解析时必然拒绝。完整方案见 [系统事件目录注册设计](../design-sys-event-catalog.md)，本记录对应
其切片 1：只交付系统目录的持久化底座，**不接线采集器**，默认接收行为逐字节不变。

## 架构决策（A2：平台契约编译进二进制）

系统事件是平台内置不可变契约：无项目归属、无 CRUD、无发布/激活/租约状态机。独立 PG 表（原
方案 A1）的事务与激活机制对其全部冗余，还会引入迁移播种与 fixture 双份真相。故采用：

- 单一真相 `contracts/fixtures/system-events.json`；
- `tooling/codegen/generate.py` 确定性序列化生成
  `backend/internal/gen/wire/system-events.gen.json`（紧凑、键排序、UTF-8 不转义）；
- `wire.SystemEvents` 经 `go:embed` 编译进二进制；
- 零迁移、零播种、零运行时配置面。未来确需运行时可配置时，解析接口不变即可替换为 PG 实现。

## 改动清单

| 文件 | 性质 | 说明 |
| --- | --- | --- |
| `tooling/codegen/generate.py` | 修改 | 追加系统事件固件生成步骤，既有 178 个 wire 类型与 bundle 输出不变 |
| `backend/internal/gen/wire/system-events.gen.json` | 生成 | 五事件的确定性嵌入式副本 |
| `backend/internal/gen/wire/bundle.go` | 修改 | 新增 `SystemEvents` embed 声明 |
| `backend/internal/ingestion/systemcatalog.go` | 新增 | `IsSystemKey`、`LoadSystemCatalog`、`Rule`、`Keys` |
| `backend/internal/ingestion/systemcatalog_test.go` | 新增 | 6 组测试 |

加载器在构造期一次性解析并编译全部属性 schema（与 `collector.loadPlan` 同一 jsonschema 编译器
模式），对以下固件缺陷一律 fail-closed：非法 JSON、格式标识不符、零事件、非保留键、重复键、
`schema_version<1`、无来源 kind、缺 schema、schema 不可编译。`Rule` 对未知键返回
`ok=false`，调用方必须拒绝而不能回退项目目录（项目目录的键约束永远不接受点号键）。

## 验证

### 单元测试（`internal/ingestion`，`-race`）

- `TestIsSystemKeyClosedPattern`：五个保留键与合法短键通过；自定义键、大小写变体、空前缀体、
  非法首字符、连字符、双点键全部拒绝；
- `TestLoadSystemCatalogEmbedded`：五键按字典序返回，版本均为 1、来源 kind 均仅 `browser`、
  schema 全部编译成功；
- `TestSystemCatalogPropertySchemaValidation`：固件自带 example 全部通过编译后 schema；
  额外属性被 `additionalProperties:false` 拒绝；`sys.page_scroll` 缺必填 `duration_ms`、
  越界比率与未知 direction 枚举均被拒绝；
- `TestSystemCatalogRuleFailsClosed`：未知 `sys.*`、普通自定义键、空串、大小写变体均
  `ok=false`；
- `TestLoadSystemCatalogRejectsCorruptFixture`：9 类损坏固件子测全部拒绝；
- `TestEmbeddedSystemCatalogMatchesCanonicalFixture`：embed 副本与
  `contracts/fixtures/system-events.json` 解析后语义深相等，防 codegen 内容漂移。

### 变异红绿取证

1. 把 `IsSystemKey` 放宽为额外接受自定义键 `page_view` →
   `TestIsSystemKeyClosedPattern` 如期变红（`non-reserved key "page_view" must not match`）；
2. 删除加载器的重复键检查 →
   `TestLoadSystemCatalogRejectsCorruptFixture/duplicate_key` 如期变红
   （`corrupt fixture "duplicate key" was accepted`）。

两次变异均以 sha256 校验逐字节还原，还原后 `internal/ingestion` 全包 `-race` 转绿。

### 生成漂移与门禁

- 连续两次运行 codegen 幂等；补跑 `gofmt -w backend/internal/gen/wire` 后，既有
  `types.gen.go`/`contracts.gen.json` 零 diff，生成目录仅新增系统事件一个文件；
- `go build ./...`、`go vet`、`gofmt` 干净；完整 `make check` 退出码 0；命名检查无禁词。

## 明确未做（切片边界）

- 采集器 `sys.` 前缀旁路、`release_revision` 系统事件口径、来源 kind 联动属切片 2；
- Kafka→ClickHouse 端到端落库与可查询验证属切片 3；
- 本切片合入后，SDK 上报 `sys.*` 仍按现状被项目目录拒绝，行为没有提前放松。
