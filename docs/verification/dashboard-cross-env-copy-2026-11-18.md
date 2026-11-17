# 看板跨环境复制重绑定验证记录（2026-11-18）

## 问题（确定性缺口）

看板复制此前只支持同项目同环境派生。规划明确要求「跨环境复制必须重新绑定并校验，不能沿用原环境权限或发布资格」。而保存分析与指标的主键是 `(project_id, analysis_id)`/`(project_id, metric_id)`，每个 ID 只属于一个环境，且全系统对跨环境引用一律 fail-closed，因此跨环境复制不可能沿用原引用，必须显式把每个可执行组件重绑定到目标环境中已发布的同种类版本。本切片交付该能力的领域模型、存储事务、HTTP 契约、客户端与控制台闭环。

## 范围与语义决策

1. **显式重绑定，无隐式匹配**：请求带 `target_environment`（必须与源环境不同的闭集值）与 `rebindings` 数组；每个可执行组件恰好一条，种类必须与源一致（analysis→analysis、metric→metric），按 `widget_id` 精确覆盖，缺/重/越界/双源/无源全部 `INVALID_DASHBOARD_REBINDING`。不按标题等业务键猜测，避免隐式错配。
2. **文本卡自动沿用**：展示型 `text_card` 不产生查询，无需重绑定条目；纯文本卡看板以空 `rebindings` 即可跨环境复制。
3. **结构先于语义**：领域纯函数 `RebindDashboardDefinition` 先做覆盖/种类/形状校验并构造新定义，存储层再逐条验证目标版本存在且属于目标环境；缺失版本复用既有 `SAVED_ANALYSIS_VERSION_NOT_FOUND`/`METRIC_VERSION_NOT_FOUND`，版本存在但环境不符返回 `DASHBOARD_REBINDING_ENVIRONMENT_MISMATCH`。
4. **参数绑定结构性保留**：`parameters` 与组件 `parameter_bindings` 原样复制；目标侧发布校验会对重绑定后的来源重新做字段存在性检查，不兼容的绑定在发布期以既有错误码拒绝，而不是静默弱化语义。副本始终是 r1 未发布草稿。
5. **幂等按目标环境隔离**：操作名 `copy:{source_id}:{target_environment}`，幂等记录写入目标环境作用域；同一幂等键可分别用于复制到不同环境而互不冲突，同环境同键仍重放、异内容仍 `IDEMPOTENCY_CONFLICT`。
6. **资源隔离不变**：副本获得全新 dashboard/widget ID，不携带发布见证、运行、归档与幂等历史；源看板环境、发布与归档状态完全不变。同环境复制路径行为保持不变，且不接受 `rebindings`。

## 改动清单

| 层 | 提交 | 文件 | 说明 |
| --- | --- | --- | --- |
| 领域 | `107021e` | `reporting/dashboard.go`、`dashboard_policy.go`、`dashboard_service.go`、`dashboard_test.go` | `DashboardRebinding`/扩展 `DashboardCopy`、`RebindDashboardDefinition` 纯函数、服务环境闭集与同环境禁重绑定、1 正常+7 拒绝单测 |
| 存储 | `66f6f7e`、`1bc91c4` | `postgres/dashboard_copy.go`、跨环境集成测试 | 目标文件夹按目标环境断言、先结构后语义的目标版本解析、环境错配错误、幂等环境作用域、gofmt |
| 契约 | `cd960f4` | `openapi.json`、生成 wire/TS、`transport/control/dashboard_test.go`、api-client | `DashboardRebinding` schema、枚举/未知字段/版本边界 400、合法载荷解码分发、客户端 44 项测试 |
| 控制台模型 | `64837aa` | `dashboards/model.ts`、`types.ts`、Playwright 模型测试 | `buildRebindings`/`executableWidgets` 纯函数、`DashboardCopyRequest`、不完整选择返回 null |
| 控制台 UI | `a115165` | `cross-env-copy.tsx`、`copy-dashboard.tsx`、`list.tsx`、`use-controller.tsx`、`dashboards.tsx`、样式 | 双模式复制弹窗、目标环境目录/版本级联选择、文本卡沿用提示、未完成禁提交 |

## 验证

### 真实 PostgreSQL 集成（`-tags=integration`，容器 `evia-development-postgres-1:55432/evia_test`）

- `TestDashboardCrossEnvironmentCopyRebindsAnalysis`：dev→staging 重绑定成功，副本落在 staging、r1 未发布、组件 ID 全新且引用切换为目标分析版本；幂等记录落在 staging 作用域；同键重放返回同一副本；同键复制到 production 生成独立副本；dev 引用用于 staging 目标 → `DASHBOARD_REBINDING_ENVIRONMENT_MISMATCH`；不存在版本 → `SAVED_ANALYSIS_VERSION_NOT_FOUND`；缺重绑定 → `INVALID_DASHBOARD_REBINDING`；目标=源环境 → `INVALID_ENVIRONMENT`；dev 文件夹锚定 staging 副本 → `DASHBOARD_FOLDER_NOT_FOUND`；源看板环境与发布状态不变。
- `TestDashboardCrossEnvironmentCopyTextOnlyNeedsNoRebinding`：纯文本卡以空重绑定跨环境成功，文本内容沿用且组件 ID 更新。
- `TestDashboardCrossEnvironmentCopyRebindsMetric`：metric 组件重绑定到 staging 指标成功且清空 analysis 字段；dev 指标用于 staging → 环境错配；metric 组件重绑定到分析版本 → `INVALID_DASHBOARD_REBINDING`。
- 同环境既有用例 `TestDashboardCopy*` 三项全部保持通过，证明原路径无回归。

### 领域、HTTP 边界与客户端

- 领域 `TestRebindDashboardDefinition*`：正常重绑定切换引用并保留文本卡；7 类结构失败（缺覆盖、未知组件、文本卡条目、重复条目、种类错配、双源、无源）全部拒绝。
- `TestDashboardHTTPCrossEnvironmentCopy`：合法跨环境载荷 201 且两个重绑定完整解码到服务；严格输入表新增非法环境枚举、重绑定未知字段、版本 0 三类 400。
- api-client 新增跨环境载荷测试，全套 44 项通过；`verify_contracts.py` 与 codegen 幂等检查通过。
- Playwright 模型套件 97 项通过，新增 `buildRebindings` 用例覆盖按种类映射、文本卡空选择合法、缺资源/缺版本返回 null。

### 变异红绿（sha256 逐字节还原，禁用 git checkout）

1. 存储层（基线 `/tmp/evia-check/xenv-copy/SHA256SUMS`）：移除目标环境断言 → 分析/指标两个错配用例同时变红；移除「目标=源环境」拒绝 → 精确的早期 `INVALID_ENVIRONMENT` 退化为后续错配错误。
2. 领域层：覆盖精确性检查置恒真 → 未知组件/文本卡条目用例变红。
3. 控制台（基线 `/tmp/evia-check/xenv-console/SHA256SUMS`）：`buildRebindings` 完整性检查置恒真 → 不完整选择不再返回 null，模型测试变红；`DashboardCopyRequest` 删除 `targetEnvironment` → tsc 在表单构造点与控制器消费点报 3 处类型错误。全部还原后 `sha256sum -c` 一致、复绿。

### 门禁

- `make check PYTHON=…/venv/bin/python` 退出码 0（首次因新集成测试未 gofmt 失败，格式化后复跑通过）；命名/契约/边界/监控/后端 `-race` 全量包与 Web（typecheck、prettier、build）全绿。
- 全量 PG 集成包 `go test -race -tags=integration -skip TestFailoverBaselineWrite ./internal/storage/postgres` 一次通过（239.7s，0 FAIL）。期间 `TestExhaustedIPCannotAllocateAccountBuckets` 在全量并发下出现一次已知连接敏感 flake，隔离复跑通过、全量复跑干净，与本切片无关；`TestFailoverBaselineWrite` 属既有多主机 DSN 门控。

## 明确未做（边界）

- 不做按标题/业务键的自动匹配，也不允许跨种类重绑定（analysis↔metric 互换）；种类切换须在目标环境先建草稿再手工调整。
- 参数绑定不在复制时按目标事件字段重映射；不兼容绑定由目标环境发布校验拒绝，复制产物是草稿而非已发布版本。
- 控制台无浏览器级组件测试设施，表单以纯函数模型测试 + tsc 变异红绿 + 生产构建为证据，端到端浏览器联调沿用既有看板工作流。
- 高级图表配置、目录搜索与侧栏复制入口仍属后续切片，与本能力正交。
