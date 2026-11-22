# 项目总览真实聚合验证（2026-11-22）

## 语义与边界

新增授权管理 API `GET /api/v1/projects/{project_id}/overview`：

- 必填 environment/from/to，时间窗最多 7 天，interval 为 hour/day。
- 人类需要当前项目成员与 `queries.read`；机器凭据同时绑定项目、环境并显式授予 `queries.read`。PostgreSQL 事务先授权，再访问事实。
- ClickHouse 读取 deduplicated `event_facts`，固定 2M rows / 256 MiB read / 512 MiB memory 等既有查询上限，参数化项目、环境、时间与分桶。
- 返回 exact total events、非空 session_id 唯一会话、非空 uid 已识别用户（不做身份图解析）、非空 anonymous_id、data watermark、小时/日趋势、Top 10 event keys。
- 不返回原始 UID/anonymous/session/event、properties 或事件行；guard breach fail-closed。

## 真实验证

- PostgreSQL `-race` `TestOverviewAuthorizationHumanAndMachineBoundaries` 通过：owner/member、机器精确权限、环境与项目隔离。
- ClickHouse `-race` `TestClickHouseProjectOverviewAggregate` 通过：dedup、项目/环境隔离、null identity/session 排除、bucket 边界、Top event 顺序/并列、watermark 与空结果。
- 首轮真实 CH 测试因 system diagnostic logs 占满磁盘（仅 996 KiB free）无法插 fixture；清理本地 system query/trace/metric/part logs 后恢复 430 MiB，测试通过。该清理不触及业务事实库。
- OpenAPI/JSON Schema 全契约验证通过；Go/TS codegen 重生成零漂移；API client 53/53。
- 完整 `make backend-test`（gofmt、vet、`go test -race ./...`）通过，新增 overview package 全绿。
- Console format/typecheck/build通过；product-composition 4/4、workspace responsive 1/1、analysis/replay models 23/23。

## 控制台

Project root 默认进入「项目总览」。首屏真实 KPI、趋势与 Top events 是主叙事；diagnostics/拒绝/探针变为次级关注。aggregate 与 diagnostics 独立加载、独立错误/空态，不以 mock 数据填充。

强制重启最新 API 与 Vite 后，以真实 newSSE 项目 development 数据捕获：事件概览已完成并显示真实总量、会话、UID、匿名标识、趋势、Top events、水位；无 document overflow。截图：`screenshots/evia-project-overview.png`。

最终 glm-5.3-flash 直接读图审阅：产品构图 PASS，阻断问题 0；诊断首页/空壳首页问题已解决。

## 剩余边界

Overview 不声称身份解析后人数、event-level completeness、转化或业务健康；拒绝趋势仍来自 diagnostics，事实趋势只表示已进入 deduplicated facts 的数据。更长历史或自定义 KPI 应走保存分析/指标/看板，不扩大 Overview 的固定范围。
