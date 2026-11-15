# 保存分析高级结果列中文表头与格式化验证记录（2026-11-15）

## 背景

留存/分群/路径/归因已可保存、发布并在控制台运行，结果统一经 `saved-analysis-run` 的通用
DataTable 呈现。但该运行器的表头映射只覆盖 trend/funnel 的少量列，四类高级分析的列
（`cohort_*`、`retained`、`retention_rate`、`segment_value`、`share`、`path`、`touchpoint`、
`credit` 等）会回退成英文原始 key，数组列还会以原始对象展示，与即席工作区的中文体验不一致。

## 改动（纯前端，无后端/契约/迁移）

`apps/console/src/saved-analysis-run/result-table.tsx`：

- 表头映射扩展为与即席工作区 `analysis.tsx` 同一套词汇，并按保存查询的 `kind` 做感知：
  `subjects` 在归因下为「转化主体数」、其余为「到达主体数」；`share` 在归因下为「归因占比」、
  分群下为「群体占比」。补齐 event/identity 列（event_time、uid、anonymous_id、context、
  properties）与全部高级列。
- 单元格渲染对齐即席语义：`share`/`conversion_rate` 按百分比展示（保留原始比例 title）、
  归因 `credit` 四位小数去尾零、`retention_rate` 数组逐项百分比后用顿号连接、`retained`
  整数数组顿号连接、`path` 字符串数组用 `→` 连接为事件路径；其余仍走通用 `displayCell`。
- 未知列保持 `column.label ?? column.key` 兜底，服务端新增列不会破坏表格。

## 正确性核对

逐一比对后端评估器实际输出的列 key（`planner.go` 与各 evaluator）：

- 留存：cohort_index/cohort_start/cohort_size/retained/retention_rate
- 分群：segment_value/subjects/share
- 路径：path/subjects
- 归因：touchpoint/subjects/share（time_decay 另含 credit）
- 漏斗：step_index/event_key/subjects/conversion_rate

前端映射覆盖全部上述 key，无臆造列；数组/数值的渲染分支与后端值类型一一对应。

## 不变量

- trend/funnel 既有表头与渲染字节行为不变，仅在映射表中新增条目与新增渲染分支。
- 不改动任何数据获取、契约或后端逻辑；通用表格、分页与结果快照机制保持原样。
- 看板组件结果（`dashboards/run.tsx`）复用同一个 `SavedAnalysisResultPresentation`，因此本修复对保存分析运行页与看板组件同时生效，单一呈现来源。

## 验证

`pnpm typecheck` 零错误、`pnpm build` 生产构建成功、prettier 格式检查通过；完整 `make check`
退出码 0（32 个 Go 包 `-race` 全 ok、零 FAIL，前端各检查与构建全过）。控制台无组件级单测设施，
本切片的正确性以后端列 key 逐一核对 + 与即席工作区已验证实现对齐 + 类型/构建门禁保证。
