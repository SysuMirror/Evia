# 留存保存分析控制台表单验证记录（2026-11-15）

## 背景

前序切片已交付留存保存模板的后端、契约与迁移能力，但人类控制台保存分析编辑器只暴露
trend/funnel 入口，留存只能通过「高级 JSON」手工编写。本切片为留存补齐专用可视化表单，
打通「选择类型 → 配置 → 客户端校验 → 保存」的控制台闭环。分群/路径/归因的专用表单按同一
分区模式续作，本切片不改动其入口。

## 改动

- `saved-analysis-model.ts`（纯逻辑层）
  - `replaceSavedKind` 增 retention 分支：切换时生成 subject=uid、day 粒度、4 期、两个待选
    边界事件的初始模板，可视化默认 `table`。
  - `savedPins` 增 retention：从 `start_event` 与 `return_event` 投影钉版，供目录加载与属性归属复用。
  - `savedAnalysisIssues` 增 retention 闭集校验：拒绝 event_versions/metrics/group_by/interval/
    funnel 等外来旋钮，强制 subject，校验 granularity（day/week）、periods（1–12）与两个边界
    事件的 event_key/schema_version，并把边界 step 纳入 pin 集合。
  - `savedDefinitionSummary` 增留存摘要。
- 编辑器分区：`types.ts` 新增 `retention` 分区与 `periods` 数字态；`section-application.ts`
  在应用时把期数收敛为 1–12 整数并把留存问题路径纳入局部校验；`use-section-editor.ts`
  打开分区时播种期数。
- 新组件 `retention.tsx`：复用既有 `PinControls` 选择起止事件版本，配主体、粒度、期数控件，
  与 funnel 分区同一交互范式。
- `configuration-fields.tsx` 类型选择器增「留存分析」，摘要区在留存类型下显示留存配置入口；
  主编辑器 `saved-analysis-editor.tsx` 在分区对话框内挂载 `RetentionSection`。

## 不变量

- trend/funnel 的表单、校验与提交字节路径不变；新分区仅在 kind=retention 时挂载。
- 客户端校验是后端 `validateRetention` 的镜像兜底，不放宽任何约束；权威校验仍在服务端
  fail-closed，前端问题只阻止本地保存按钮。
- 可视化兼容函数中 `table` 对所有 kind 成立，留存默认且仅需要表格，未新增图表类型。

## 验证

控制台没有组件单元测试设施，故对真实编译产物做运行时断言，而非另写副本：

1. 用 `tsc` 把 `saved-analysis-model.ts` 及其自包含依赖 `analysis-model.ts`（`@evia/api-client`
   仅 type-only，编译期擦除）临时编译为 CommonJS。
2. Node 断言覆盖：类型切换生成正确骨架；完整钉版的留存定义 `savedAnalysisIssues` 返回零问题；
   `savedPins` 返回两个边界 pin；六个失败用例（缺 subject、坏粒度、期数 13、起始事件未钉版、
   混入 metrics、顶层 event_versions）各自命中预期问题路径。全部通过。
3. `pnpm typecheck`（tsc --noEmit）零错误，`pnpm build` 生产构建成功，prettier 全量格式检查通过。

## 边界

本切片只交付留存专用表单。分群/路径/归因在后端与契约上已可保存，控制台仍可通过「高级 JSON」
配置并经同一套客户端/服务端校验保存；三者的专用分区表单作为后续同构切片。
