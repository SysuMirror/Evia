# 浏览器导航类型闭集提取（navigation_type）验证记录（2026-11-19）

## 问题（确定性缺口）

`EventEnvelope.context.navigation_type` 五值闭集（`initial/push/replace/pop/restore`）的 Go 侧唯一校验点是 `pipeline/record.go` 的裸字符串 switch，此前没有命名常量、没有单测直接覆盖、也没有与 OpenAPI 枚举的机械对齐；SDK 侧虽有客户端拒绝，服务端入队校验一旦漏改某值会产生静默放宽。

## 方案（行为不变的纯机械收敛）

1. 新增 [navigation_policy.go](../../../backend/internal/pipeline/navigation_policy.go)：`NavigationInitial/Push/Replace/Pop/Restore` 常量、`NavigationTypes` 切片与 `ValidNavigationType`，作为 Go 侧单一真相源；空字符串合法（SDK 省略即普通 push）的规则保留在 record 校验器，不进入 `ValidNavigationType`；
2. `record.go` 的 switch 改用常量，空值分支语义不变；
3. 在既有 `TestRecordValidationCanonicalizationAndScope` 中补两类直接覆盖：五个合法值在结构体字段与 ContextJSON 保持一致时全部接受，未知值 `reload`（两处同步设置）被 switch 拒绝。记录校验要求去规范化结构体与上下文 JSON 字节一致，因此用例必须同时设置 `Context.NavigationType` 与 `ContextJSON`；
4. 不改动任何 JSON 夹具、生成代码与运行时行为。

## 关联词汇的取舍说明

- `Diagnostic.stage`（received/validated/queryable/identity_resolved/rejected）：全仓没有任何 Go 产出点，词汇仅存在于契约，建立集合相等守卫会产生零生产者的虚假覆盖感，跳过；待诊断阶段状态机落地后再提取。
- `EventPlan.status`（draft_example_not_published/published）：Go 侧无字面量，状态由发布流程与 SQL 侧承载，同理跳过。
- 采集回执 `ItemResult.Status`（accepted/rejected）是独立词汇，与 Diagnostic.stage 的 rejected 仅同形，不合并。

## 常驻守卫

新增 [navigation_contract_test.go](../../../backend/internal/pipeline/navigation_contract_test.go)：`pipeline.TestNavigationTypeVocabularyMatchesContract` 读嵌入 bundle，断言嵌套路径 `EventEnvelope.context.navigation_type` 枚举与 `NavigationTypes` 排序后集合相等，零外部依赖。

## 守卫有效性取证（变异红绿）

- 基线 sha256 存 `/tmp/evia-check/navigation/sha256-baseline.txt`；
- 向 `NavigationTypes` 注入 `"drift_probe"` → 报 `navigation type drift: contract 5 != Go 6 [drift_probe ...]` 变红；
- 备份还原后 `sha256sum -c` 全部 OK，复跑复绿，`grep drift_probe/DriftProbe` 零残留。

## 验证

- `go build ./...`、`go vet ./...`（含全部测试编译）通过；
- 全量 `make check` EXIT=0、零 FAIL（Go 33 包、client 51、sdk 63、console 18、生产构建），日志：`/tmp/evia-check/navigation/make-check.log`；本切片仅触及 pipeline 记录校验与纯单测，无数据库/外部依赖路径。

## 明确未做（边界）

- 不改变空值合法的既有规则、不改动 SDK 侧校验与 JSON 夹具；
- 不改变任何运行时行为、契约内容、API 形状或数据库约束。
