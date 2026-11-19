# 查询结果精度闭集提取（metadata.accuracy）验证记录（2026-11-19）

## 问题（确定性缺口）

查询结果精度标记三值闭集（`exact/approximate/not_applicable`）此前以字符串字面量分散在 analysis 的校验器与 planner、reporting 的复合公式对齐器共 3 个生产文件 17 处：planner 为非指标引擎写 `not_applicable`、精确路径写 `exact`、近似聚合写 `approximate`，validation 另有「仅 unique/p50/p90/p95/p99 允许 approximate」的跨字段规则。OpenAPI `QueryResult.metadata.accuracy` 枚举是契约侧唯一副本，Go 侧没有单一真相源，也没有机械对齐守卫。

## 方案（行为不变的纯机械收敛）

1. 新增 [accuracy_policy.go](../../../backend/internal/analysis/accuracy_policy.go)：`AccuracyExact/Approximate/NotApplicable` 常量、`AccuracyValues` 切片与 `ValidAccuracy`，作为 Go 侧单一真相源；
2. 生产侧 17 处全部改为消费常量：analysis 包内非限定，reporting 用 `analysis.` 前缀；近似算子的跨字段规则仍留在 validation.go，闭集只固化词汇本身；
3. 测试侧 23 个文件、58 行 Go 结构体字段/比较/赋值按同一五种模式批量迁移（analysis 包内非限定，外部包 `analysis.` 前缀）；
4. 刻意保留：1 处原始 JSON 字符串夹具中的 `"accuracy":"exact"`（wire 解码的独立契约见证）。

## 关联词汇的取舍说明

`QueryResult.metadata.coverage.state`（provisional/settled/partial）经评估**不做** Go 闭集提取：当前 Go 侧只产出 `provisional`（clickhouse 引擎默认值与复合公式对齐器的空值兜底），`settled/partial` 是为未来管线完整性与身份解析能力预留的契约值，没有 Go 产出路径；为单值写点建立三值集合相等守卫会给出虚假覆盖感。该词汇继续由 OpenAPI 枚举约束，待管线完整性工作包落地状态机后再提取。

## 常驻守卫

新增 [accuracy_contract_test.go](../../../backend/internal/analysis/accuracy_contract_test.go)：`analysis.TestAccuracyVocabularyMatchesContract` 读嵌入 bundle，断言嵌套路径 `QueryResult.metadata.accuracy` 枚举与 `AccuracyValues` 排序后集合相等，零外部依赖。

## 守卫有效性取证（变异红绿）

- 基线 sha256 存 `/tmp/evia-check/accuracy/sha256-baseline.txt`；
- 向 `AccuracyValues` 注入 `"drift_probe"` → 报 `accuracy drift: contract 3 != Go 4 [approximate drift_probe ...]` 变红；
- 备份还原后 `sha256sum -c` 全部 OK，复跑复绿，`grep drift_probe/DriftProbe` 零残留。

## 验证

- `go build ./...`、`go vet ./...`（含全部测试编译）通过；
- 全量 `make check` EXIT=0、零 FAIL（Go 33 包、client 51、sdk 63、console 18、生产构建），日志：`/tmp/evia-check/accuracy/make-check.log`；
- 真实 ClickHouse 集成（`-race -tags integration ./internal/analysis`）83.634s 通过，覆盖 exact/approximate/not_applicable 三种精度在真实存储上的产出；
- 真实 PostgreSQL 集成（`-tags integration -skip TestFailoverBaselineWrite ./internal/storage/postgres`）104.916s 通过，覆盖查询结果元数据的持久化与读取；`TestFailoverBaselineWrite` 仍是与本切片无关的既有环境门控。

## 明确未做（边界）

- 不改动 JSON 夹具与生成代码；
- 不改变任何运行时行为、契约内容或 API 形状；
- approximate 的算子白名单规则与 coverage.state 预留值不在本切片范围。
