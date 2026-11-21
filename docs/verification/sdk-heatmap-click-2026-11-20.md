# 浏览器 SDK 点击热力采集验证记录（2026-11-20）

## 交付

- 系统目录新增浏览器保留事件 `sys.heatmap_click`（schema v1）：`x_ratio`/`y_ratio`（0..1）、`viewport_width`/`viewport_height`（1..65535）、`element_path`（1..128）、可选 `element_index`（0..255）。系统事件目录由 5 项扩至 6 项，公开配置上限相应从 205 调到 206。
- SDK 新增 opt-in `autoHeatmapClicks` 与可注入 `HeatmapHost`。浏览器默认在 document capture phase 监听 click；键盘激活无指针坐标时使用目标元素中心。
- 元素路径只使用 tag、受限 role 与 `nth-of-type`，最多四层；不收集文本、输入值、id、class、href 或任意属性。每分钟最多 120 条，畸形/超限样本在入队前丢弃；复用现有队列、重试、逐事件回执和系统目录发布门。

## 验证

- SDK node:test 全套 76/76 通过（原 73 + 3）：默认关闭零监听；启用后属性钳制/四位小数/可选 index；畸形样本丢弃、滚动一分钟第 121 条被限速；destroy 取消监听。
- newSSE 实际接入启用 `autoHeatmapClicks`，真实浏览器点击页面标题：SDK 发 `POST /v1/collect`，响应 202 + 逐事件 `accepted` 回执。示例属性：`x_ratio=0.3748`、`y_ratio=0.0744`、`viewport=1366×900`、`element_path=div:nth-of-type(1)>...>h1:nth-of-type(1)`；write key 在证据中脱敏。
- ClickHouse 直查确认同一事件落库，route_name 与页面上下文、session/sequence 一并保留。

## 边界

本切片只交付点击采集；热力聚合（坐标网格/元素路径聚合）、分析 API 和控制台可视化尚未实现。会话回放是独立协议与存储子系统，不属于本切片。
