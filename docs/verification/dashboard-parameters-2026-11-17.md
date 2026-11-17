# 看板通用参数绑定验证记录（2026-11-17）

## 问题（确定性缺口）

长期方案要求看板支持可复用的运行时参数：参数在看板级声明一次，组件显式绑定到自己固定来源的字段，运行时再提供值，且「相同显示名称不能自动绑定、缺少映射 fail-closed、参数不能删除固定约束」。此前看板只有逐组件 `widget_filters`，没有统一参数契约、绑定关系和编译器，`docs/plans/custom-dashboards.md` 5.1 仍标注该能力未实现。本切片交付通用参数的领域内核、数据库门控、存储编译、契约代码生成与控制台编辑/运行闭环。

## 范围与语义决策

1. **参数不存字段伪全局名**：`ParameterDeclaration{parameter_id,label,type,operator,required,default}` 只描述值契约（≤12 个，标识 1–64 字符、标签 1–128 字符）；字段只出现在组件的 `ParameterBinding{parameter_id,field}` 上，field 限定该组件固定来源的 system 字段或带 event_key/schema_version 的 property 字段。
2. **闭集运算**：string=`eq/neq/in/prefix/contains`，number=`eq/neq/in/gt/gte/lt/lte`，boolean=`eq/neq`；数组值仅 `in` 接受，1–20 个同构元素，字符串 ≤1024 字符，数值限 safe integer 且拒绝非有限值。
3. **两层 fail-closed**：`NormalizeDashboardDefinition` 在草稿/发布期校验声明闭集、唯一标识、绑定声明引用与字段形状；`NormalizeDashboardExecution` 在运行边界拒绝未知键与类型不符；按组件编译器 `CompileWidgetParameterFilters` 再做必填/默认值/重复字段校验，存储调用方无法绕过。
4. **不新增引擎路径**：参数编译结果是普通 `analysis.Filter`；合并顺序固定为 模板固定过滤 AND 参数绑定 AND 组件局部过滤（`CombineWidgetRuntimeFilters`），随后走既有 Bind/validateFilter 字段与预算校验，绑定字段在固定版本上不存在时运行失败而非执行。
5. **json.Number 兼容**：HTTP 边界与运行规范化使用 `UseNumber`，内核同时接受 `float64` 与 `json.Number`，词法值原样进入作业请求。
6. **迁移 037**：替换 `dashboard_definition_valid`，对 parameters 与 parameter_bindings 做 PL/pgSQL 形状门控（闭集类型/运算、声明引用、参数与字段去重、字段 pin 完整性），CHECK 约束先删后建；无参数段的旧定义完全兼容。

## 改动清单

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 领域内核 | `reporting/parameter.go`、`parameter_test.go` | 声明/绑定/值模型、编译器、边界值校验、过滤合并、json.Number 适配 |
| 领域 | `reporting/dashboard.go`、`dashboard_policy.go`、`dashboard_test.go` | 定义/执行/运行结构接入 parameters、parameter_bindings、parameter_values；规范化与发布 diff |
| 迁移 | `037_dashboard_parameters.sql` | 形状校验函数替换，参数/绑定闭集与引用完整性门控 |
| 存储 | `postgres/dashboard_runs.go` | 每组件编译参数谓词并与局部过滤合并；指标/分析/比较期三路径复用；读回还原 parameter_values |
| 契约 | `openapi.json`、`query-request.schema.json` | 新增 ParameterDeclaration/ParameterBinding/ParameterValues 与可复用 `$defs/field`；codegen 重生（186 wire 类型） |
| 控制层 | `transport/control/dashboard_test.go` | 参数契约正负样本，非法形状边界 400 |
| 集成测试 | `postgres/dashboard_parameter_integration_test.go` | SQL 门控样本与端到端运行/拒绝用例 |
| 控制台 | `dashboards/parameter-*.ts(x)`、`editor.tsx`、`run.tsx`、`model.ts`、`styles.css` | 声明编辑器、组件绑定弹窗、运行参数输入与执行体接线 |

## 验证

### 真实 PostgreSQL 集成（`-tags=integration`）

- `TestDashboardParameterShapeGate`：对 `dashboard_definition_valid` 直接驱动 3 个正样本（无参数、空段、完整 property+system 绑定）与 11 个负样本（非数组、超 12 个、类型/运算越界、标识重复、缺必填键、绑定未声明参数、同参重复、同字段重复、property 缺 pin、未知 field kind、system 带 pin），全部符合预期。
- `TestDashboardParameterRunCompilesFiltersAndRoundTrips`：带必填 number 参数的看板发布后运行，子作业 `request.filter` 确认为 `gte=5` 且字段为 `lesson_id`；读回 run 回显 `parameter_values`（json.Number 形态）；同键重放返回同 run，改值同键返回 `IDEMPOTENCY_CONFLICT`。
- `TestDashboardParameterRunMergesAheadOfLocalFilter`：同时给参数值与组件局部过滤时，作业 filter 为 AND，args 顺序严格为参数谓词在前、局部过滤在后。
- `TestDashboardParameterRunFailClosed`：缺必填 `MISSING_PARAMETER_VALUE`、未知键 `INVALID_PARAMETER`、类型不符 `INVALID_PARAMETER_VALUE`；可选参数无值无默认时不产生 filter 谓词。
- `TestDashboardParameterBindingRejectedAtDraftSave`：绑定未声明参数在创建草稿时即 `INVALID_PARAMETER_BINDING`。

### 领域与边界单测

- `parameter_test.go`：编译成功/六类拒绝/同构数组/json.Number（含 `1e999`、非法数字）/边界值映射/合并顺序共 8 个测试。
- `dashboard_test.go`：定义期参数规范化 7 类拒绝、运行时值边界 3 类拒绝。
- `TestDashboardHTTPParameterContracts`：合法参数化定义与运行值 201/202 通过；null 值、object 值、未知类型、未知 field kind、额外声明属性 5 类在 HTTP 边界 400，不到达服务。

### 变异红绿（sha256 逐字节还原）

1. 内核短路未知参数值检查 → `TestCompileParameterFailClosedCases` 变红，还原后 `sha256sum -c` OK 复绿；
2. 内核短路必填检查 → 缺必填用例变红，还原复绿；
3. 迁移 037 删除「绑定必须引用已声明参数」谓词（基线 `381ba1c2…`）→ SQL 门控负样本被接受而变红，还原哈希一致后复绿；
4. 存储层把编译结果置空（基线 `b8d27a8f…`）→ 端到端断言子作业缺参数谓词而变红，还原哈希一致后复绿；
5. 控制台运行参数组件类型错配（基线 `4e5ea7ef…`）→ tsc TS2345/TS2352 变红，还原哈希一致后复绿。

### 门禁

- `make check PYTHON=…/venv/bin/python` 退出码 0：命名/契约/边界/监控/后端测试/Web（typecheck、prettier、build）全绿；`verify_contracts.py` 契约校验通过，api-client 41 项测试通过，codegen 幂等（`generated-check` 无 diff）。
- 全量 PG 集成包 `go test -count=1 -tags=integration -skip TestFailoverBaselineWrite ./internal/storage/postgres/...` 新鲜通过（90.7s，0 FAIL）。`TestFailoverBaselineWrite` 属既有多主机环境门控，与本切片无关。
- 首轮满负载运行时 `TestAccessTransactionsAndIsolation` 出现一次容器连接重置（`connection reset by peer`），隔离重放立即通过、全量重跑收敛，判定为环境瞬时抖动而非代码缺陷。

## 明确未做（边界）

- 参数绑定不做保存分析「分析自身声明参数」的间接映射，v1 直接绑定组件固定来源字段；多事件分析需逐字段绑定的语义留给后续切片。
- 控制台无组件级单测设施，交互以 tsc 类型守卫（含变异红绿）、构建与后端真实 PG 集成测试为证据。
- 看板复制、高级图表配置仍为独立缺口，不在本切片范围。
