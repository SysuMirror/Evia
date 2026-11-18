# 采集来源词汇闭集提取（Source.kind / Source.status）验证记录（2026-11-19）

## 问题（确定性缺口）

采集来源的两组词汇——类型 `browser/server` 与状态 `active/disabled`——此前以字符串字面量分散在 10 个生产文件：

- `kind` 18 处：ingestion 的来源服务/采集器、catalog 来源校验、pipeline 记录规范化、postgres 存储、delivery 授权分派、evia-bench 客户端；
- `status` 6 处：采集准入与 delivery 授权的 fail-closed 停用判定、来源更新校验、诊断停用清单。

OpenAPI 的 `Source`/`SourceInput`/`SourceIssued`/`SourceUpdate` 四个 schema 共 6 个枚举属性是契约侧副本，Go 侧没有单一真相源，也没有机械对齐守卫。同形字符串还被其他词汇复用（归档视图 `view=active`、诊断分区 `disabled`、账号停用日志、CSV 列名 `active`），盲目全局替换会误伤，必须逐行按语义分类。

## 方案（行为不变的纯机械收敛）

1. 新增 [source_policy.go](../../../backend/internal/ingestion/source_policy.go)：`SourceKindBrowser/Server` + `SourceKinds/ValidSourceKind`、`SourceStatusActive/Disabled` + `SourceStatuses/ValidSourceStatus`，作为 Go 侧单一真相源；
2. 生产侧 24 处全部改为消费常量/助手：校验链改为 `ValidSourceKind/ValidSourceStatus`，switch 与 map 键改用常量；
3. 测试侧迁移全部 Go 结构体字段、比较、切片与助手入参（ingestion 包内非限定，外部包 `ingestion.` 前缀），共 20 个测试文件；
4. 刻意保留的同形异义/夹具：
   - 测试中的原始 JSON 字符串与 ClickHouse 行 map 夹具（wire/存储的独立见证）；
   - `platform/contract` 测试的字面量（该包被 ingestion 依赖，反向导入成环，且其本身就是契约见证）；
   - 归档视图 `active/archived`、诊断分区状态、账号停用日志字段、CSV 数据列名、身份物化阶段、幂等键字符串、测试用例名称；
   - FactKind 三值闭集（`user_intent/client_observed/server_confirmed`）是独立词汇，本切片不触碰。

## 常驻守卫

新增 [source_contract_test.go](../../../backend/internal/ingestion/source_contract_test.go)：`ingestion.TestSourceVocabularyMatchesContract` 读嵌入 bundle，遍历四个 Source schema 的 6 个枚举属性（kind×3、status×3），分别与 `SourceKinds`/`SourceStatuses` 排序后集合相等，并断言恰为 3+3 个属性，schema 重构导致覆盖面缩水时同样变红。

## 守卫有效性取证（变异红绿）

- 基线 sha256 存 `/tmp/evia-check/source-vocab/sha256-baseline.txt`；
- 变异一：向 `SourceKinds` 注入 `"drift_probe"` → 报 `Source.kind source vocabulary drift: contract [browser server] != Go [browser drift_probe server]` 变红；
- 变异二：向 `SourceStatuses` 注入 `"drift_probe"` → 报 `Source.status source vocabulary drift: contract [active disabled] != Go [active disabled drift_probe]` 变红；
- 两次变异均经备份还原，`sha256sum -c` 全部 OK，复跑复绿，`grep drift_probe/DriftProbe` 零残留。

## 验证

- `go build ./...`、`go vet ./...`（含全部测试编译）通过；
- 全量 `make check` EXIT=0、零 FAIL（Go 33 包、client 51、sdk 63、console 18、生产构建），日志：`/tmp/evia-check/source-vocab/make-check.log`；
- 真实 PostgreSQL 集成（`-tags integration -skip TestFailoverBaselineWrite ./internal/storage/postgres`）103.822s 通过，覆盖来源创建/更新/停用、机器目录来源授权与交付激活全链路；`TestFailoverBaselineWrite` 仍是与本切片无关的既有环境门控。

## 明确未做（边界）

- 不改动 JSON/行夹具、生成代码与同形异义词汇；
- 不改变任何运行时行为、契约内容、API 形状或数据库约束；
- `Source.application_state`（pending/applied/failed）与 `FactKind` 三值是独立闭集，留待后续切片。
