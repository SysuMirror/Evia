# 单值枚举契约守卫（catalog 语义标记 / artifact 语言）验证记录（2026-11-19）

## 问题（确定性缺口）

契约中存在两个当前只有单值的枚举，Go 侧以裸字符串产出，没有任何机械对齐保证；单值闭集与多值闭集存在同构漂移风险——未来加项时漏改不会在构建期暴露：

1. `AnalysisCatalogField.missing_semantics`（`["absent_key"]`）与 `null_semantics`（`["present_but_not_a_typed_value"]`）：[discovery.go](../../backend/internal/reporting/discovery.go) 构建分析目录字段时内联产出；
2. `TypeScriptArtifact.language`（`["typescript"]`）：[artifacts.go](../../backend/internal/transport/control/artifacts.go) 组装 wire 响应时内联产出。

## 方案（行为不变的纯机械收敛）

### reporting 域：catalog 字段语义

- 新增 [catalog_semantics_policy.go](../../backend/internal/reporting/catalog_semantics_policy.go)：`CatalogMissingSemanticsAbsentKey`/`CatalogNullSemanticsPresentButNotATypedValue` 常量与 `CatalogMissingSemantics`/`CatalogNullSemantics` 单值切片；
- 新增 [catalog_semantics_contract_test.go](../../backend/internal/reporting/catalog_semantics_contract_test.go)：`TestCatalogFieldSemanticsVocabularyContract` 读嵌入 bundle，对两个 `AnalysisCatalogField` 的单值 enum 分别做集合相等断言；
- discovery 生产赋值与 discovery_test 断言迁移到常量。

### control 域：artifact 语言

- 新增 [artifact_policy.go](../../backend/internal/transport/control/artifact_policy.go)：包内非导出 `artifactLanguageTypeScript` 常量（与同包 `quota*` 词汇风格一致，无外部引用故不导出）与 `artifactLanguages` 切片；
- 新增 [artifact_language_contract_test.go](../../backend/internal/transport/control/artifact_language_contract_test.go)：`TestArtifactLanguageVocabularyContract` 断言 `TypeScriptArtifact.language` 单值 enum 与切片集合相等；
- artifacts.go 的 wire 赋值与 artifacts_test 比较迁移到常量。
- 刻意保留：`internal/artifacts/typescript.go` 生成源码中的 `// language: typescript` 注释是产物内容而非 wire 词汇；测试中的 URL 路径字符串是路由不是枚举。

## 守卫有效性取证（变异红绿）

基线 sha256（四个新文件）存 `/tmp/evia-check/single-value/sha256-baseline.txt`，原件备份于 `baseline/`。

- 向 `CatalogMissingSemantics` 注入 `"drift_probe"` → 报 `catalog field missing_semantics drift: contract [absent_key] != Go [absent_key drift_probe]` 变红；
- 向 `artifactLanguages` 注入 `"drift_probe"` → 报 `artifact language drift: contract [typescript] != Go [drift_probe typescript]` 变红；
- 双双从备份还原，`sha256sum -c` 四文件 OK，复跑复绿，`grep drift_probe internal/reporting internal/transport/control` 零残留。

## 验证

- gofmt 零差异；两域守卫单测通过；
- 全量 `make check` EXIT=0、零 FAIL：Go 33 包 ok（`go test -race ./...`）、api-client 51、browser-sdk 63，命名/契约/边界/prettier/typecheck/生产构建全过。日志：`/tmp/evia-check/single-value/make-check.log`；
- 本切片仅触及 unit-tested 包（reporting/control），无迁移、无集成面变更；同会话内 PG（250.4s/250.9s）、CH（84.5s）、pipeline 核心两测的真实依赖回归结果仍覆盖各自集成包。

## 明确未做（边界）

- 不改动契约、wire 生成代码与任何运行时行为；
- 不提前声明未实现的语义值或语言——单值切片如实反映当前产出，新增值时由守卫强制双侧同动；
- `artifacts.LanguageVersion`（`"5.0"`）是版本基线而非语言枚举，不在本切片范围。
