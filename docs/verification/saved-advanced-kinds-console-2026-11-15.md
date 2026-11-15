# 分群/路径/归因保存分析控制台表单验证记录（2026-11-15）

## 背景

前序切片已让分群/路径/归因在后端、契约与迁移层可保存发布，并为留存补齐了控制台专用表单。
本切片把同一套分区编辑器模式同构推广到剩余三类，至此高级分析（留存/分群/路径/归因）在
人类控制台的「选择类型 → 配置 → 客户端校验 → 保存」闭环全部打通，不再依赖手写高级 JSON。

## 改动

- 纯逻辑层 `saved-analysis-model.ts`
  - `replaceSavedKind` 增三类初始骨架（均默认 subject=uid、可视化 table）：分群带系统标量字段、
    路径 depth=3 无起始事件、归因 last_touch + 86400 秒窗口 + 1 触点。
  - `savedPins` 按各自类型块投影钉版：分群=分群事件；路径=可选起始事件（无则零 pin）；
    归因=转化 + 全部触点，供目录加载与属性归属复用。
  - `savedAnalysisIssues` 增三类闭集校验，镜像后端 `validateSegment`/`validatePath`/
    `validateAttribution`：拒绝趋势旋钮与兄弟配置块、强制 subject，分别校验标量字段闭集、
    路径深度 2–8 与可选起始、归因模型闭集/窗口/半衰期/触点 1–8。
  - 摘要补三类人话描述。
- 分区体系：`types.ts` 增 segment/path/attribution 分区与 depth/halfLife 数字态；
  `section-application.ts` 收敛路径深度、归因窗口与 time_decay 半衰期，并把三类问题路径纳入
  局部校验；`use-section-editor.ts` 打开分区时播种对应数字。
- 三个新组件 `segment.tsx`/`path.tsx`/`attribution.tsx`：复用 `PinControls` 选事件版本、
  `FieldSelect` 选分群标量字段、`Checkbox` 控路径折叠、`Select`/`Input` 控归因模型与数值，
  触点与起始事件支持增删；主编辑器与类型选择器、摘要入口接线。

## 类型边界处理

`FieldSelect` 面向较宽的 `QueryField`（系统字段含 `event_time`），而分群标量字段闭集不含
`event_time`。组件在边界把返回值收窄为 segment 字段类型，是否合法仍由 `savedAnalysisIssues`
对系统 key 闭集 fail-closed 复核，后端 `validateSegment` 为最终权威，不因 UI 收窄而放宽约束。

## 不变量

- trend/funnel/retention 的表单、校验与提交字节路径不变；新分区仅在对应 kind 挂载。
- PinControls 回写沿用 `{...step, ...pin}`，保留高级 JSON 中可能存在的 step 级 filter。
- 客户端校验只阻止本地保存按钮，服务端权威校验不变。

## 验证

控制台无组件单测设施，对真实编译产物做运行时断言：用 tsc 把 `saved-analysis-model.ts` 及其
自包含依赖临时编译为 CommonJS，Node 断言覆盖——

- 三类 `replaceSavedKind` 生成正确骨架且默认 table；
- 完整钉版定义 `savedAnalysisIssues` 零问题，`savedPins` 计数正确（分群 1、有起始路径 1、
  无起始路径 0、归因=转化+触点 3）；
- 失败路径：分群缺 subject/系统字段 event_time/未钉版事件/混入 metrics，路径深度越界/缺
  subject/起始未钉版，归因未知模型/窗口 0/空触点/time_decay 缺半衰期，各自命中预期问题路径。

`pnpm typecheck` 零错误、`pnpm build` 生产构建成功、prettier 全量格式检查通过。

## 边界

本切片只交付控制台表单与镜像校验，不改动任何后端、契约或迁移；高级分析的持久化、依赖固定
与发布哈希仍由前序切片的服务端逻辑与真实 PostgreSQL 集成测试保证。
