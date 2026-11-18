# 部署环境闭集收敛（environment）验证记录（2026-11-19）

## 问题（确定性缺口）

部署环境三值闭集（`development/staging/production`）是全仓出现频率最高的词汇表：嵌入 bundle 中有 **54 个** schema 属性（`environment` 与 `target_environment`）携带同一枚举，但 Go 侧没有单一真相源，27 个生产文件以四种形态各自维护副本：

1. 四个包私有 `validEnvironment` 函数（retention/delivery/quotas/reports）与两个导出 `ValidEnvironment`（catalog/ingestion）；
2. 12 处 `!= "development" && ...` 内联链（analysis/access/pipeline/reporting/control/postgres/budget/admissionstats）；
3. 8 处 `switch case "development", ...`（归档/回补/擦除/导出作业存储与服务）；
4. 2 处 `[]string{...}` 切片（budget 遍历、admissionstats 包级变量）。

任一副本漏改都会造成「契约允许但服务拒绝」或反向漂移，且此前没有任何机械对齐保证。

## 方案（行为不变的纯机械收敛）

1. 新增横切叶子包 [env](../../../backend/internal/platform/env/env.go)：`Development/Staging/Production` 常量、`Environments` 切片与 `Valid(string) bool`，零内部依赖；
2. 27 个生产文件全部改为消费该包：既有包内/导出助手保留为单行委托（公共 API 形状不变、调用点零改动），内联链改为 `env.Valid(...)`，switch 改用常量，切片改为 `env.Environments`；evia-bench 原本限定单环境的校验改用 `env.Development`；
3. 生产侧 `grep '"development"'`（排除测试与生成代码）零残留；
4. 刻意保留的字面量：
   - 测试夹具中的环境字符串（122 个测试文件、693 处）：属于输入/见证数据而非校验闭集，与 raw SQL 字面量同理由保留，守卫本身提供防漂移保证；
   - 生成代码 `internal/gen/wire` 与迁移 SQL：由 codegen 门禁与数据库约束各自兜底。

## 常驻守卫

新增 [contract_test.go](../../../backend/internal/platform/env/contract_test.go)：`env.TestEnvironmentVocabularyMatchesEveryContractSchema` 递归遍历嵌入 bundle 的全部 `$defs`，对每个名为 `environment`/`target_environment` 且带字符串枚举的属性断言与 `Environments` 排序后集合相等；另设覆盖下限（`checked >= 30`，当前实际 54），schema 大规模重构导致覆盖面缩水时同样变红。

## 守卫有效性取证（变异红绿）

- 基线 sha256 存 `/tmp/evia-check/env-closed-set/sha256-baseline.txt`；
- 变异一：向 `Environments` 注入 `"drift_probe"` → 报 `DashboardVersion.environment environment drift: contract 3 != Go 4` 变红；
- 变异二：把覆盖下限从 30 改为 100 → 报 `expected the environment vocabulary on dozens of schemas, only checked 54` 变红，证明下限保护有效；
- 两次变异均经备份还原，`sha256sum -c` 全部 OK，复跑复绿，`grep drift_probe/DriftProbe` 零残留。

## 验证

- `go build ./...`、`go vet ./...`（含全部测试编译）通过；
- 全量 `make check` EXIT=0、零 FAIL（Go 32 包、client 51、sdk 63、console 18、生产构建），日志：`/tmp/evia-check/env-closed-set/make-check.log`；
- 真实 PostgreSQL 集成回归（`-tags integration -skip TestFailoverBaselineWrite`）：postgres 整包 106.267s 通过，覆盖归档/回补/擦除/导出作业存储、保存分析/看板/指标环境校验与发现面列表等全部经环境闭集把关的路径；
- 唯一未运行项 `TestFailoverBaselineWrite` 是既有环境门控（未设置 `EVIA_TEST_PG_FAILOVER_DSN` 即 t.Fatal，干净基线同样失败），与本切片无关。

## 明确未做（边界）

- 不改动测试夹具、raw SQL、迁移与生成代码；
- 不改变任何运行时行为、契约内容、API 形状或数据库约束；
- 保留各包既有助手名称（委托到 `env.Valid`），避免跨包调用点的无价值 churn。
