# 事件事实类型闭集提取（DraftInput.fact_kind）验证记录（2026-11-19）

## 问题（确定性缺口）

事件草稿的事实类型三值闭集（`user_intent/client_observed/server_confirmed`）此前以内联字符串存在于 `catalog/validation.go` 的两处校验：发布必填与闭集校验合写在一条长布尔链中，`server_confirmed` 与浏览器来源的互斥规则又重复一次字面量。OpenAPI `DraftInput.fact_kind` 枚举是契约侧唯一副本，Go 侧没有单一真相源，也没有机械对齐守卫。

## 方案（行为不变的纯机械收敛）

1. 新增 [fact_kind.go](../../../backend/internal/catalog/fact_kind.go)：`FactKindUserIntent/ClientObserved/ServerConfirmed` 常量、`FactKinds` 切片与 `ValidFactKind`，作为 Go 侧单一真相源；
2. `validation.go` 的长布尔链简化为 `!ValidFactKind(text(in.FactKind))`，互斥规则改用常量；
3. 测试侧迁移 5 处 Go 结构体字段（catalog 包内非限定，artifacts/postgres/delivery 用 `catalog.` 前缀并补导入）；
4. 刻意保留：delivery 测试中经 `json.Marshal` 的 `map[string]any` 夹具（`fact_kind` 键值是 wire 层独立见证，与 JSON 字符串夹具同理由保留）。

## 关联词汇的取舍说明

`Source.application_state`（pending/applied/failed）经评估**不做** Go 闭集提取：该值由 PostgreSQL 只读计算（`CASE WHEN observed_at+interval '2 seconds'<=clock_timestamp() THEN 'applied' ELSE 'pending' END`，见 `postgres/sources.go` 的 `sourceColumns`），Go 侧仅在创建时写入初始 `pending`，`failed` 从不经 Go 产出；为单值写点建立三值闭集与集合相等守卫会给出虚假的覆盖感。该词汇继续由 SQL 表达式与 OpenAPI 枚举双侧约束，如未来出现 Go 侧状态机再提取。

## 常驻守卫

新增 [fact_kind_contract_test.go](../../../backend/internal/catalog/fact_kind_contract_test.go)：`catalog.TestFactKindVocabularyMatchesContract` 读嵌入 bundle，断言 `DraftInput.fact_kind` 枚举与 `FactKinds` 排序后集合相等，零外部依赖。

## 守卫有效性取证（变异红绿）

- 基线 sha256 存 `/tmp/evia-check/fact-kind/sha256-baseline.txt`；
- 向 `FactKinds` 注入 `"drift_probe"` → 报 `fact kind drift: contract 3 != Go 4 [client_observed drift_probe ...]` 变红；
- 备份还原后 `sha256sum -c` 全部 OK，复跑复绿，`grep drift_probe/DriftProbe` 零残留。

## 验证

- `go build ./...`、`go vet ./...`（含全部测试编译）通过；
- 全量 `make check` EXIT=0、零 FAIL（Go 33 包、client 51、sdk 63、console 18、生产构建），日志：`/tmp/evia-check/fact-kind/make-check.log`；
- 真实 PostgreSQL 集成（`-tags integration -skip TestFailoverBaselineWrite ./internal/storage/postgres`）105.342s 通过，覆盖事件草稿校验、发布与目录制品全链路；`TestFailoverBaselineWrite` 仍是与本切片无关的既有环境门控。

## 明确未做（边界）

- 不改动 map/JSON 夹具与生成代码；
- 不改变任何运行时行为、契约内容、API 形状或数据库约束；
- `server_confirmed` 与浏览器来源互斥等跨字段规则仍留在 validation.go，闭集只固化词汇本身。
