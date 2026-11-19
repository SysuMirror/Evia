# 兼容性判定与指标引用类型闭集提取验证记录（2026-11-19）

本记录覆盖两个同批处理的小词汇：`ValidationResult.compatibility`（3 值）与 `MetricReference.kind`（2 值）。

## 问题（确定性缺口）

- **兼容性判定**：`catalog.Compatibility` 的三个返回值（initial/compatible/breaking）以裸字符串散落在 `compatibility.go` 的 8 个 return 点，OpenAPI `ValidationResult.compatibility` 枚举是契约侧唯一副本，无单一真相源、无守卫；
- **指标引用类型**：`reporting.MetricRefKindAtomic/Composite` 常量与 `NormalizedMetricRefKind` 归一化已存在，但没有可供守卫消费的闭集切片，也没有与 `MetricReference.kind` 枚举的机械对齐；空 kind 归一为 atomic 的历史兼容规则使该词汇更需要防漂移。

## 方案（行为不变的纯机械收敛）

1. 新增 [compatibility_policy.go](../../../backend/internal/catalog/compatibility_policy.go)：`CompatibilityInitial/Compatible/Breaking` 常量、`CompatibilityValues` 切片与 `ValidCompatibility`；`compatibility.go` 8 个 return 全部改用常量；
2. `reporting/saved_analysis.go` 在既有常量旁补 `MetricRefKinds` 切片作为守卫真相源，归一化逻辑与空值兼容规则不变；
3. 测试侧迁移全部 Go 比较与表驱动期望值（catalog 包内非限定，control/postgres 用 `catalog.` 前缀）；`provision_integration_test.go` 中作为 Bootstrap 用户名的 `"initial"` 是无关同形词，刻意保留；
4. 不改动任何 JSON 夹具、生成代码与运行时行为。

## 常驻守卫（2 个新测试）

- `catalog.TestCompatibilityVocabularyMatchesContract`：`ValidationResult.compatibility` 枚举与 `CompatibilityValues` 集合相等；
- `reporting.TestMetricReferenceKindVocabularyMatchesContract`：`MetricReference.kind` 枚举与 `MetricRefKinds` 集合相等。

两者均读嵌入 bundle（`wire.Schemas`），零外部依赖。

## 守卫有效性取证（变异红绿）

- 基线 sha256 存 `/tmp/evia-check/compat-refkind/sha256-baseline.txt`；
- 向 `CompatibilityValues` 注入 `"drift_probe"` → 报 `compatibility drift ... Go 4 != contract 3` 变红；
- 向 `MetricRefKinds` 注入 `"drift_probe"` → 报 `metric reference kind drift ... Go 3 != contract 2` 变红；
- 两次变异均经备份还原，`sha256sum -c` 全部 OK，复跑复绿，`grep drift_probe/DriftProbe` 零残留。

## 验证

- `go build ./...`、`go vet ./...`（含全部测试编译）通过；
- 全量 `make check` EXIT=0、零 FAIL（Go 33 包、client 51、sdk 63、console 18、生产构建），日志：`/tmp/evia-check/compat-refkind/make-check.log`；
- 真实 PostgreSQL 集成（`-tags integration -skip TestFailoverBaselineWrite ./internal/storage/postgres`）103.644s 通过，覆盖草稿校验兼容性判定与指标引用版本钉选全链路；`TestFailoverBaselineWrite` 仍是与本切片无关的既有环境门控。

## 明确未做（边界）

- 兼容性判定的保守规则（元数据变更即 breaking、required 只减不增等）仍留在 `compatibility.go`，闭集只固化判定结果词汇；
- 指标引用的空值归一化、互斥与不可变版本钉选规则不变；
- 导航类型 `navigation_type` 中同形的 `"initial"` 属独立词汇，不在本切片范围。
