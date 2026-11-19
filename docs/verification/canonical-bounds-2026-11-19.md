# 跨包常量单一真相源与组合根不变量守卫验证记录（2026-11-19）

本记录覆盖两个同属「防跨包漂移」主题的纯逻辑/测试切片：JSON 安全整数界收敛、系统保留事件预算绑定。两者均无迁移、无契约变更、无运行时行为变化。

## 切片一：JSON 安全整数界（2^53-1）单一真相源

### 问题（确定性缺口）

JSON 安全整数上界 `9007199254740991` 在改动前分散于 **9 个生产位置独立维护**：`canonical.MaxInteger`、`ingestion.MaxRevision`、`catalog.MaxInteger`、`delivery.MaxRevision`、`access.MaxMembershipRevision`、`pipeline.maxInteger`、`analysis.MaxSafeInteger` 七个常量，加 `httpapi`/`control/retention`/`reports`/`quotas`/`evia-bench` 等处的内联字面量。任一副本单独修改都会让溢出策略在 API 边界之间静默分裂。`platform/canonical` 本就是零内部依赖、被 20+ 包引用的底层包，是天然规范源，但此前没有任何测试把这些副本钉在一起。

### 方案（纯标识符收敛，值字节不变）

1. `canonical` 新增**无类型**源常量 `MaxSafeInteger = 9007199254740991`，既有 typed API 改为 `MaxInteger int64 = MaxSafeInteger`。无类型是因为 analysis 在 int64 与 float64 两种量级比较中都引用该界；
2. 七个本地常量改为 `= canonical.MaxInteger`（analysis 用无类型 `= canonical.MaxSafeInteger`），五处内联字面量改为直接引用，调用点零改动；
3. 全仓生产 Go 代码现仅余两处字面量：规范源本身，与 `postgres/settings.go` 中 raw SQL 文本 `settings_revision<9007199254740991`（查询文本无法引用 Go 常量，沿用本仓 raw SQL 边界）。

### 守卫（双层，职责互补）

- `canonical.TestSafeIntegerBoundPinned`：把源值钉到数学定义 `1<<53-1`，并断言无类型/typed 两个导出相等——捕获**规范源本身**漂移；
- `cmd/evia.TestSafeIntegerBoundIsSingleSource`（组合根）：把 ingestion/catalog/delivery/access/analysis 五个包的本地别名逐一断言等于 `canonical.MaxInteger`——捕获**本地别名**与规范源分叉。变异证明二者互补：改源值时数学钉扎变红而组合根仍绿（别名一致跟随），改本地别名时组合根变红。

## 切片二：系统保留事件预算跨包绑定

### 问题

`delivery.ReservedEventLimit = 5` 是硬编码常量，注释自称「由系统事件夹具不变量钉住」，但实际无测试连接它与嵌入的 `SystemCatalog` 真实大小；`PublicConfigEventLimit = ProjectReleaseEventLimit + ReservedEventLimit` 据此派生。若向生成夹具加入第 6 个 `sys.*` 事件，公开配置预算会静默错配（实际 206 而预算仍 205）。ingestion 包内虽有夹具计数 `!= 5` 测试，但与 delivery 常量各写一份魔数，跨包漂移无人捕获。

### 方案（仅新增测试）

`cmd/evia.TestReservedEventLimitPinsEmbeddedCatalog`（组合根，cmd/evia 本就同时组装 ingestion 与 delivery）：

1. `LoadSystemCatalog()` 真实加载嵌入目录，断言规则数 `== delivery.ReservedEventLimit`；
2. 经生产 `systemEventProvider` 适配后断言暴露事件数一致、键与目录一一对应（防适配层静默丢键）；
3. 断言 `PublicConfigEventLimit == ProjectReleaseEventLimit + 实际保留事件数`。

## 守卫有效性取证（变异红绿）

基线 sha256 分存 `/tmp/evia-check/safe-int/` 与 `/tmp/evia-check/sys-catalog/`，原件备份于各自 `baseline/`。

- 安全整数变异 A：`ingestion.MaxRevision` 改为 `canonical.MaxInteger - 1` → 组合根报 `ingestion.MaxRevision=9007199254740990 diverges from canonical.MaxInteger=9007199254740991` 变红；
- 安全整数变异 B：规范源改为 9007199254740990 → `TestSafeIntegerBoundPinned` 报 `must equal 2^53-1=9007199254740991` 变红（组合根因别名一致跟随而正确保持绿，证明双层职责分离）；
- 系统目录变异：`ReservedEventLimit` 5→6 → 报 `delivery.ReservedEventLimit=6 but embedded catalog has 5 rules` 变红；
- 三次变异均 sha256 逐字节还原，复跑全绿，`grep` 零残留。

## 验证

- gofmt 零差异；`go build ./...` 通过；
- 全量 `make check` EXIT=0、零 FAIL：Go 33 包 ok（`go test -race ./...`，含全部受影响包）、api-client 51、browser-sdk 63、prettier/typecheck/生产构建全过。日志：`/tmp/evia-check/safe-int/make-check.log`、`/tmp/evia-check/sys-catalog/make-check.log`；
- 无需真实依赖回归：切片一是编译期等价的标识符替换（所有值仍为同一整数），切片二仅新增测试，均无迁移、无 SQL/契约/网络面变更。

## 明确未做（边界）

- 不修改任何常量数值、SQL 文本、契约或运行时行为；
- raw SQL 中的安全整数界与既有 CHECK/查询字面量保留，不强行参数化；
- 未触及孤立的 `AuditEntry`/`AuditList` HTTP schema（result allowed/denied/failed + request_id），其实现是独立的运维审计语义特性（需决策 denied/failed 记录口径与 request_id 传播），不在本防漂移切片范围。
