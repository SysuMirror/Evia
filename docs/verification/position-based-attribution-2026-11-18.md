# 位置归因（position-based U 型）模型验证记录（2026-11-18）

## 问题（确定性缺口）

归因模型族已交付 `last_touch`、`first_touch`、`linear`、`time_decay` 四个成员，`position_based` 在契约枚举与 Go 校验中作为「真正未实现」的 fail-closed 负例占位（4 处测试引用）。本切片把它落地为第五个显式闭集成员，补齐业界标准的 U 型多触点模型，不新增采集、存储、迁移或自由请求参数。

## 范围与语义决策

1. **固定 U 型 40/20/40，不引入请求参数**：权重是确定性常量而非可配置项，与前四个成员的闭集枚举风格一致，避免扩大契约面。规则按每个转化主体的**去重后、窗内、按首次出现排序**的触点键计算：
   - 1 个触点：满权 1.0；
   - 2 个触点：0.5/0.5（保证总权为 1，而非 0.4+0.4=0.8）；
   - n≥3：首/末触点各 0.4，中间 n−2 个均分 0.2（各得 `0.2/(n−2)`）。
   每个主体权重和恒为 1；无窗内触点的转化仍归入固定 `__direct__` 桶满权，转化人群不丢失。
2. **复用既有加权骨架**：新增 `evaluatePositionBasedAttribution` 与 `positionBasedWeights` 纯函数，输出复用 linear/time_decay 的 `linearCredit` 累加器与 `touchpoint/subjects/credit/share` 四列形状，排序口径（credit 降序、触点字典序）一致。
3. **planner 同步放开 credit 列**：加权模型（linear/position_based/time_decay）输出 fractional `credit`，单触模型（last/first）仍只出三列、字节稳定。
4. **负例字符串轮换**：`position_based` 转正后，原 4 处 fail-closed 负例（1 处 Python 契约校验 + 3 处 Go 测试）统一改用真正未实现的 `data_driven`，保持「未知模型 fail-closed」守护不被削弱。
5. **控制台同构接入**：即席分析与保存分析编辑器两个归因选择器同步加项，标签显式写明 40/20/40；position_based 无半衰期字段，复用 linear 的无额外参数路径。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 契约 | `contracts/query/query-request.schema.json` | model 枚举加 `position_based` 并补 U 型语义描述；重跑 codegen（Go 离线 bundle `contracts.gen.json`、TS `schema.gen.ts` 两处联合类型） |
| 领域 | `internal/analysis/attribution.go`、`validation.go`、`planner.go` | 新增 `positionBasedWeights` 与评估器、校验闭集放开、planner credit 列条件加 position_based |
| 领域测试 | `internal/analysis/attribution_test.go` | 5 个新用例 + 3 处 Go 负例轮换为 `data_driven` |
| 真实 CH 集成 | `internal/analysis/clickhouse_integration_test.go` | 新增 `TestClickHousePositionBasedAttribution` |
| 契约工具 | `tooling/verify_contracts.py` | 保存分析负例模型轮换为 `data_driven` |
| 控制台 | `analysis-model.ts`、`analysis.tsx`、`saved-analysis-model.ts`、`saved-analysis-editor/attribution.tsx`、`tests/browser/analysis-model.spec.ts` | 类型联合、两个选择器、两处标签映射、保存分析闭集校验、模型测试 |

## 验证

### 领域单测（`go test -race ./internal/analysis`）

新增 5 个用例全部 verbose PASS：

- `TestPositionBasedWeightsTable`：n=1/2/3/4/5 的首/末/中间权重与总和为 1（n=4 中间各 0.1、n=5 中间各 0.2/3）。
- `TestEvaluatePositionBasedUShapeWeightsAndDirectBucket`：三触点 0.4/0.2/0.4、两触点 0.5/0.5、单触点满权、无触点 direct 满权；聚合 credit `ad_click=1.9`/`email_open=0.7`/`social_click=0.4`/`direct=1.0`，share 和为 1。
- `TestEvaluatePositionBasedDedupesRepeatedTouchpoint`：重复键去重为单节点满权。
- `TestEvaluatePositionBasedRespectsLookbackWindow`：窗外触点剔除、1 秒窗塌缩为 direct。
- `TestPositionBasedValidationAndPlannerContract`：契约解析接受、Go 校验通过、plan 暴露 credit 列。

### 真实 ClickHouse 集成（`-tags=integration`，独立隔离库 + 真实迁移视图）

- `TestClickHousePositionBasedAttribution`（verbose 0.18s PASS）：四个主体分别走三/二/单/零触点路径，经真实引擎断言四列输出、`subjects` 与 fractional `credit`（1.9/0.7/0.4/1.0）、share 和为 1。
- 全量 analysis 集成包 `go test -race -tags=integration -count=1 ./internal/analysis/` 一次通过（73.2s，EXIT=0，0 FAIL）。

### 控制台

- Playwright 模型套件 103 项通过（新增 `attribution passes the position-based U-shape model through without a half-life`），断言请求 `model: 'position_based'` 且不带 `decay_half_life_seconds`。
- `tsc --noEmit` 通过；prettier 全过；生产构建在 make check 内通过。

### 变异红绿（sha256 逐字节还原，禁用 git checkout）

后端两轮（基线 `/tmp/evia-check/position-attribution/SHA256SUMS`，2 文件）：

1. `positionBasedEdgeWeight` 0.4→0.3 → 权重表 n=3 总和变 0.8、评估器 ad_click 1.8≠1.9，两个测试精确变红。
2. planner 移除 position_based 的 credit 列条件 → 契约测试变红（plan 缺 credit 列）。

前端一轮（基线 `/tmp/evia-check/position-attribution-fe/SHA256SUMS`，2 文件）：

3. 移除 `analysis-model.ts` 类型联合中的 `position_based` → tsc 报 TS2322（模型测试赋值不被接受）。

每轮还原后 `sha256sum -c` 全部 OK 并复跑复绿；最终 tsc `--noEmit` 与全量单测再次通过。

### 门禁

- `make check PYTHON=…/venv/bin/python` 一次通过（EXIT=0）：命名、契约（含 `data_driven` 负例，64 个保存分析负向用例）、边界、监控、Go `-race` 全量包（analysis 15.6s）、prettier、typecheck、test:client 49、SDK 57、生产构建全绿。
- codegen 后 wire/离线 bundle 与 TS schema 均为本次枚举扩展的预期 diff，无手改生成文件。

## 明确未做（边界）

- 权重固定为 40/20/40，不做请求内可配置比例、Markov/Shapley/数据驱动等更复杂模型；`data_driven` 仍为 fail-closed 未实现值。
- 仅即席归因与保存分析模板的模型选择，不改变 1–8 触点、1–604800 秒窗口、十万行有界拉取等既有护栏。
- 不做渠道归并、跨身份合并；热力图/会话回放仍受采集协议与专用存储阻塞。
- 控制台无组件级单测设施，选择器交互以纯函数模型测试 + tsc 变异红绿 + 生产构建为证据；全栈 browser-test 需凭证运行时，不在默认门禁。
