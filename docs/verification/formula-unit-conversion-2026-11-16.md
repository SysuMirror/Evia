# 公式显式单位换算验证记录（2026-11-16）

## 问题（确定性缺口）

复合公式的单位代数是保守的封闭系统：普通单位标签只做标注、永不改变数值。这导致一个真实的语义不可能——一个 `millisecond`（毫秒）原子指标无法重新表达为 `second`（秒）：把它除以 1000 仍推导为 `millisecond`（带单位量除以无量纲数保留原单位），声明结果单位为 `second` 又会因与推导单位不一致被拒。时长类指标因此无法在毫秒与秒之间对齐或相加。规划文档明确要求「毫秒与秒必须显式换算」。本切片用一个受限的一元换算节点封闭该缺口，不引入开放量纲系统。

## 范围与语义决策

1. **新增一元 `convert` 节点，而非隐式自动换算**：节点显式携带一个子表达式（`left`）与 `target_unit`，没有字符串语法。只有显式写出的换算才改变数值，避免单位标签被误解为自动转换。
2. **封闭线性因子表是唯一扩展点**：`formulaConversions` 当前只定义 `millisecond→second`（×0.001）与 `second→millisecond`（×1000）。字节、事件、UV、`number` 等没有线性因子的单位，以及恒等转换（ms→ms）一律 fail-closed；未来加入新单位换算只需扩表，求值与推断路径不变。当前不存在币种单位，故无货币换算。
3. **因子仅由目标单位决定**：单位推断阶段已证明子表达式单位是目标唯一对应的同量纲来源，因此数值求值器只需按 `target_unit` 查因子，无需把静态单位穿透到数值层。换算结果仍走 NaN/无穷/安全整数检查，越界归 null；null 子值向上传播。
4. **声明单位必须等于推导单位**：`VerifyFormulaUnit` 对 convert 返回 `target_unit`，所以公式声明的结果单位与目标不一致时仍被拒，单位标签无法静默改值。
5. **预算与既有形态不变**：convert 计入现有节点/深度/引用预算，仍是显式树；线网契约只在 `FormulaExpression.op` 枚举增加 `convert` 并新增可选 `target_unit`，其余节点形状零变化，既有公式逐字节兼容。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 领域 | `reporting/formula.go` | `FormulaConvert` 常量、`TargetUnit` 字段与严格解码、一元节点规范化、封闭换算表与因子辅助 |
| 语义 | `reporting/formula_semantics.go` | convert 单位推断（同量纲 fail-closed）；求值乘目标因子，null/越界安全 |
| 契约 | `openapi.json` | op 枚举加 `convert`、`target_unit`（枚举 millisecond/second）、节点描述 |
| 生成 | wire bundle/types、`schema.gen.ts` | codegen，无手写生成文件 |
| 控制台 | `formula-metrics/model.ts`、`formula-metrics.tsx` | convert 运算符、目标单位选择器（替代右操作数）、回填与提示 |
| 测试 | `formula_convert_test.go`、`formula_metrics_integration_test.go` | 纯领域换算单测与真实 PG 发布/拒绝集成 |

## 验证

### 纯领域单测（`internal/reporting`）

新增 `formula_convert_test.go`：规范化接受合法 convert 且只保留 left/target，拒绝缺目标、未知目标（byte）、缺子节点、携带 right 的畸形节点；单位推断覆盖 ms→s、s→ms 合法，以及 byte→s、event→ms、ms→ms 恒等、number→s 四类拒绝；`VerifyFormulaUnit` 验证声明单位须等于推导目标；求值验证 2500ms→2.5s、3s→3000ms，null 传播与 `(1500ms+1500ms)→3s` 的嵌套换算。

### 真实 PostgreSQL 集成（隔离 schema，`-tags=integration`）

新增 `TestFormulaMetricUnitConversion`：先发布一个 `sum(lesson_id)`、单位 `millisecond` 的原子指标；发布 `convert(msMetric → second)`、结果单位 `second` 成功，`LoadFormulaExecution` 计划保留 convert 节点与目标单位且只扇出一个原子来源；恒等转换 ms→ms、把 event 计数指标换算成秒（跨量纲）、声明单位与目标不一致三类草稿在校验快照阶段全部 fail-closed，无法发布。

### 变异红绿（三轮，sha256 逐字节还原）

基线哈希记录于 `/tmp/evia-check/formula-convert-baseline.sha256`：

1. 互换换算表两个因子 → 求值用例 `millisecond_to_second`/`second_to_millisecond` 同时变红；
2. 单位推断对 convert 直接返回目标、移除来源校验 → 恒等与跨量纲拒绝用例变红（被错误放行）；
3. 求值跳过因子直接返回子值 → 因子用例与嵌套换算用例变红。

三轮均用 Edit 精确还原并 `sha256sum -c` 两个文件全部 OK，重跑 reporting 包转绿。

### 门禁

- `pnpm typecheck` 零错误、`pnpm build` 成功、prettier 通过；
- `go build ./...` 与 reporting 单测通过；codegen 幂等；gofmt/命名检查无禁词；
- 完整 `make check` 与全量 PG 集成包结果见提交时复核。

## 明确未做（边界）

- 仅毫秒↔秒一对同量纲换算；字节倍率（KB/MB）、币种等需先定义单位与因子，不在封闭表内即拒绝；
- 不做隐式自动换算，两个不同单位的加减仍按既有规则要求显式 convert 对齐后才能运算；
- 看板直接引用指标版本、身份关联图、热力图/会话回放仍为独立缺口；
- 控制台无组件级单测设施，前端以 tsc 严格类型、生产构建与既有请求槽模式保证；全栈浏览器 e2e 属凭证门控，不在默认 `make check`。
