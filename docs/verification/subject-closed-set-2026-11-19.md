# 查询主体闭集提取（subject）验证记录（2026-11-19）

## 问题（确定性缺口）

查询主体词汇（`uid/anonymous`，外加契约预留的 `resolved_person`）此前以字符串字面量分散在 analysis 校验器、planner、trend_facts 与 reporting 指标策略、能力发现共 5 个生产文件：同一词汇既有 `r.Subject != "uid" && r.Subject != "anonymous"` 形式的六处重复闭集判断，也有 planner 中与 ClickHouse 列名（`uid`/`anonymous_id`）混用的局部变量。OpenAPI 中 `MetricDefinition.subject`（2 值）与 `QueryRequest.subject`（3 值，含预留值）两个枚举是契约侧副本，Go 侧没有单一真相源，也没有机械对齐守卫。

## 方案（行为不变的纯机械收敛）

1. 新增 [subject_policy.go](../../../backend/internal/analysis/subject_policy.go)：`SubjectUID/SubjectAnonymous/SubjectResolvedPerson` 常量、`Subjects` 切片（仅两个已实现主体）与 `ValidSubject`，作为 Go 侧单一真相源；预留值有独立常量但显式不进入 `ValidSubject`，校验继续 fail-closed；
2. 生产侧 24 处替换：六处重复闭集判断收敛为 `ValidSubject`，预留值拒绝点、planner 的 `r.Subject`/`metric()` 参数比较与 discovery 广告切片改用常量；
3. 测试侧 25 个文件、80 余处 Go 结构体字段/赋值/比较迁移（analysis 包内非限定，外部包 `analysis.` 前缀），含 3 行多赋值语句的手工处理；
4. 刻意保留的同形异义/夹具：
   - ClickHouse 列名：planner 中 `subject := "uid"`/`key := "uid"` 局部变量承载的是列名（另一分支为 `anonymous_id`），不是主体枚举；`systemTypes`、`needed["uid"]`、`json:"uid"` tag、探针列类型 map、identity_candidates/raw_access 的字段名同理保留；
   - erasure 包的 `SubjectUID/SubjectAnonymousID`（`anonymous_id` 列名词汇）是独立闭集，不合并；
   - 测试中的原始 JSON 字符串夹具（`"subject":"uid"` 等）作为 wire 解码的独立见证保留。

## 常驻守卫

新增 [subject_contract_test.go](../../../backend/internal/analysis/subject_contract_test.go)：`analysis.TestSubjectVocabularyMatchesContract` 读嵌入 bundle，分别断言：

- `MetricDefinition.subject` 枚举与 `Subjects`（uid/anonymous）集合相等；
- `QueryRequest.subject` 枚举与 `Subjects + SubjectResolvedPerson` 三值集合相等，锁定预留值只能是 `resolved_person`。

既有 reporting discovery 守卫的子集断言（discovery 只广告已实现主体）保持有效，其切片现引用同一组常量。

## 守卫有效性取证（变异红绿）

- 基线 sha256 存 `/tmp/evia-check/subject/sha256-baseline.txt`；
- 变异一：向 `Subjects` 注入 `"drift_probe"` → 报 `metric subject drift: contract [anonymous uid] != Go [anonymous drift_probe uid]` 变红；
- 变异二：把 `SubjectResolvedPerson` 改为 `"drift_probe"` → 报 `query subject drift: contract [anonymous resolved_person uid] != Go [anonymous drift_probe uid]` 变红，证明两个枚举分支均有独立守护；
- 两次变异均经备份还原，`sha256sum -c` 全部 OK，复跑复绿，`grep drift_probe/DriftProbe` 零残留。

## 验证

- `go build ./...`、`go vet ./...`（含全部测试编译）通过；
- 全量 `make check` EXIT=0、零 FAIL（Go 33 包、client 51、sdk 63、console 18、生产构建），日志：`/tmp/evia-check/subject/make-check.log`；
- 真实 ClickHouse 集成（`-race -tags integration ./internal/analysis`）82.949s 通过，覆盖 uid/anonymous 双主体在真实存储上的查询与列选择；
- 真实 PostgreSQL 集成（`-tags integration -skip TestFailoverBaselineWrite ./internal/storage/postgres`）整包 105.662s 通过。首轮曾出现一次 `TestSavedAnalysisQueryConcurrentReplayAndCurrentAuthorization` 的 55432 端口 `connection reset by peer`（测试自建 PG 实例的连接层错误，非断言失败）；该测试隔离复跑 0.493s 通过、整包复跑全绿，判定为基础设施抖动而非本切片回归；`TestFailoverBaselineWrite` 仍是与本切片无关的既有环境门控。

## 明确未做（边界）

- 不改动列名变量、JSON tag、探针 schema 与 JSON 夹具；
- 不改变任何运行时行为、契约内容、API 形状或数据库约束；
- `resolved_person` 的身份图实现仍属未交付语义能力，本切片只固化其预留状态与拒绝行为的防漂移机制。
