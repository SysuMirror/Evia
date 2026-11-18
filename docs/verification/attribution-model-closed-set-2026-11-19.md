# 归因模型闭集提取（attribution.model）验证记录（2026-11-19）

## 问题（确定性缺口）

归因查询的五个模型（`last_touch/first_touch/linear/position_based/time_decay`）此前以字符串字面量分散在 analysis 包三个生产文件：`validation.go` 的闭集校验、`attribution.go` 的评估器分派与首触分支、`planner.go` 的加权模型列判定。OpenAPI `QueryRequest.attribution.model` 枚举是契约侧唯一副本，Go 侧没有单一真相源，也没有机械对齐守卫——新增模型时校验器、评估器、planner 任一处漏改都会产生静默语义偏差。

## 方案（行为不变的纯机械收敛）

1. 新增 [attribution_policy.go](../../../backend/internal/analysis/attribution_policy.go)：`AttributionModel*` 五常量、`AttributionModels` 切片与 `ValidAttributionModel`，作为 Go 侧单一真相源；
2. 生产侧 6 处全部改为消费常量/助手：校验器改为 `!ValidAttributionModel(a.Model)`，评估器 switch 与首触分支、planner 加权模型判定改用常量；
3. 测试侧 17 处 Go 结构体赋值与比较改用常量（analysis 包内非限定、postgres/reporting 用 `analysis.` 前缀）；
4. 刻意保留：8 处原始 JSON 夹具中的 `"model":"..."` 字符串——它们是 wire 解码的独立契约见证，与 raw SQL 字面量同理由保留；控制台 TS 侧由 codegen 生成的联合类型兜底，不在本切片范围。

## 常驻守卫

新增 [attribution_contract_test.go](../../../backend/internal/analysis/attribution_contract_test.go)：`analysis.TestAttributionModelVocabularyMatchesContract` 读嵌入 bundle，断言嵌套路径 `QueryRequest.attribution.model` 枚举与 `AttributionModels` 排序后集合相等，零外部依赖。

## 守卫有效性取证（变异红绿）

- 基线 sha256 存 `/tmp/evia-check/attribution-models/sha256-baseline.txt`；
- 向 `AttributionModels` 注入 `"drift_probe"` → 报 `attribution model drift: contract 5 != Go 6 [drift_probe ...]` 变红；
- 备份还原后 `sha256sum -c` 全部 OK，复跑复绿，`grep drift_probe/DriftProbe` 零残留。

## 验证

- `go build ./...`、`go vet ./...`（含全部测试编译）通过；
- 全量 `make check` EXIT=0、零 FAIL（Go 33 包、client 51、sdk 63、console 18、生产构建），日志：`/tmp/evia-check/attribution-models/make-check.log`；
- 真实 ClickHouse 集成（`-race -tags integration ./internal/analysis`）83.119s 通过，覆盖五模型在真实存储上的评估与 planner 列形态；
- 真实 PostgreSQL 集成（`-tags integration -skip TestFailoverBaselineWrite ./internal/storage/postgres`）104.650s 通过，覆盖保存分析归因模板的入库与读取；`TestFailoverBaselineWrite` 仍是与本切片无关的既有环境门控。

## 明确未做（边界）

- 不改动 JSON 夹具、控制台 TS 与生成代码；
- 不改变任何运行时行为、契约内容或 API 形状；
- 归因模型的新增（如数据驱动模型）仍属语义决策，本切片只固化既有五值的防漂移机制。
