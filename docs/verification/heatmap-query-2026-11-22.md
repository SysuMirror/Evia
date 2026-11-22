# 有界点击热力图查询与控制台验证（2026-11-22）

## 语义

- ad-hoc `kind=heatmap` 固定读取 `sys.heatmap_click` schema v1。
- 必须选择一个精确 `route_name` 或 `route_template`，以及精确 viewport width/height；不混合移动/桌面布局。
- 固定 20×10 网格，左上为原点；`x_ratio/y_ratio=1` 收口到末格。每次点击只贡献一次，返回非零格 `x_bin/y_bin/clicks`，按 y/x 排序。
- 时间范围最多 7 天、limit 至少 200。拒绝 event selections、filter、subject/identity、group_by、metrics、interval 等通用面，保持固定有界语义。
- aggregate heatmap 不要求 raw permission，不返回 UID/session/page/event/element_path；项目与环境仍由既有查询事务授权。

## 实现

- query contract/domain/kind policy/validation/planner/clickhouse aggregate 分支完整接线。
- 参数化 SQL 固定 event/version，防御性排除错误 JSON 类型、越界坐标、错误 viewport 和空 route；提取匹配行最大 received_at watermark。
- migration 048 把 `analysis_query_jobs.kind` 约束扩到全部当前 ad-hoc kinds 与 heatmap，修复既有数据库约束漂移。
- 控制台增加「点击热力图」模式、精确 route/viewport 表单、20×10 蓝色顺序色阶、数值图例、非零格 tooltip 语义与精确结果表。不伪造页面截图或 DOM overlay。

## 验证

- focused Go analysis/reporting/storage tests通过。
- TypeScript typecheck 与 console production build通过。
- Playwright analysis-model 19/19：heatmap strict request、固定网格、方向和蓝色色阶通过。
- 真实 ClickHouse `-race` `TestClickHouseBoundedHeatmapExactScopeBinsAndWatermark` 通过：边界坐标、route/viewport/project/environment/version 隔离、dedup、错误属性排除、顺序与 watermark 均按预期。
- 真实 PostgreSQL `-race` `TestHeatmapJobPersistsThroughExpandedKindConstraint` 通过。
- 全栈控制台以 newSSE 项目 development、`route_name=Home`、`1440×1000` 运行真实 query-worker：返回两格 `(17,0)=4`、`(11,2)=5`，共 9 次匹配点击；UI 标记 completed/exact，网格深浅与精确表一致。
- 移动端仿真使用 Chromium `390×844`、deviceScaleFactor=3、isMobile/touch；同一路由按精确移动视口查询返回一格 `(9,5)=1`，控制台按 390:844 纵向比例渲染，证明桌面/移动事实不会混合。
- 截图：`screenshots/evia-click-heatmap-console.png`、`screenshots/evia-click-heatmap-mobile.png`。

## 边界

尚未做 element_path 排名、多 viewport 合并、页面 screenshot/DOM overlay、滚动文档热图、平滑/核密度、保存分析/看板，以及百万 click 专项容量矩阵。以上不得从本切片推断已支持。
