# 版本化指标

指标把一套统计口径保存成独立资源：固定事件版本、行级筛选、统计主体、聚合、精度和单位。服务端支持创建草稿、修订比较、差异校验、不可变发布和指定版本运行。普通 UI 和 Agent 使用相同 API。

当前是原子指标。公式、单位换算、指标之间的依赖，以及保存分析/看板直接引用指标版本仍待交付。现有保存分析继续固定事件版本，已有看板不会被新指标或指标草稿修改。

## 统计口径

| 聚合 | 单位与含义 |
| --- | --- |
| `count` | `event`，选定事件的次数；只有明确绑定业务页面事件时才表示业务 PV |
| `unique`，未指定字段，`subject: uid` | `raw_uid`，原始登录标识去重，排除缺失/空 UID |
| `unique`，未指定字段，`subject: anonymous` | `anonymous_id`，匿名标识去重，不代表已经合并为自然人 |
| `unique`，指定字段 | `distinct_value`，该字段的不同值数量 |
| `sum`、`avg`、`min`、`max`、`p95` | 必须指定支持的数值字段，单位为 `number`、`millisecond`、`second` 或 `byte` |

默认精确，只有 `unique` 和 `p95` 可以显式选择 `accuracy: approximate`。单位与聚合不兼容时拒绝保存。数值单位由作者声明，服务端不会自动推断业务含义，也不会把毫秒转换成秒；修改单位会产生发布 diff。当前没有货币及复合量纲运算。

每个指标固定 1–20 个已发布事件版本，每个事件只选一个版本。草稿可以先引用待发布版本，但校验和发布必须证明这些版本属于当前项目与环境，并校验属性类型。历史版本查询不要求该事件仍是当前激活版本。系统事件运行时注册及身份图仍未实现。

## 接口与发布流程

令 `P = /api/v1/projects/{project_id}`，`M = P/metrics/{metric_id}`。权威字段见 [OpenAPI](../contracts/http/openapi.json)。

| 方法与路径 | 行为 | 机器权限 |
| --- | --- | --- |
| `GET P/metrics` | 按必填 `environment` 分页，`limit` 默认 20，最大 100 | `metrics.read` |
| `POST P/metrics` | 创建草稿，必需 `Idempotency-Key`，返回 201 | `metrics.write` |
| `GET M`、`GET M/draft` | 读取资源或草稿 | `metrics.read` |
| `PATCH M/draft` | 替换完整 definition，必需 `If-Match: "revision"` | `metrics.write` |
| `POST M/validations` | 返回规范化定义、事件依赖、diff 和校验摘要，不执行查询 | `metrics.write` |
| `GET M/versions`、`GET M/versions/{version}` | 读取不可变版本，版本列表默认 20，最大 50 | `metrics.read` |
| `POST M/versions` | 发布指定草稿，必需 `Idempotency-Key`，返回 201 | `metrics.publish` |
| `POST M/versions/{version}/queries` | 指定版本异步入队，必需 `Idempotency-Key`，返回 202 查询任务 | `metrics.run` 和 `queries.run` |

人类 owner/editor 可编辑、校验和发布；viewer 可读取和运行聚合。会话写请求仍需 CSRF。读取查询任务/结果使用现有查询接口和 `queries.read`；涉及身份字段时额外检查 `queries.raw.read`，不能通过指标筛选或分组绕过。机器创建后的权限不可变，原 `analyses.*` 权限不隐含新增的 `metrics.*`。

创建示例使用已发布的合成页面事件：

```json
{
  "environment": "development",
  "definition": {
    "title": "页面登录访客",
    "description": "按业务页面事件统计原始 UID",
    "event_versions": [{ "event_key": "page_opened", "schema_version": 1 }],
    "subject": "uid",
    "aggregation": { "op": "unique", "accuracy": "exact" },
    "unit": "raw_uid"
  }
}
```

读取草稿得到 `draft_revision`、`published_version` 后，用 `{draft_revision, expected_published_version}` 请求校验。检查 diff，再把同一组版本和 `validation_hash` 提交给发布接口。发布事务重新读取草稿、依赖和基线，不把摘要当作授权。过时修订或基线返回 409，需要重读并重新校验。

创建和发布重试返回最初接受的快照；相同幂等键配不同内容返回冲突。响应丢失时保留原请求和幂等键。草稿更新通过修订比较防止覆盖，遇到未知结果先读取当前草稿。发布不会改写旧版本，也不会激活事件来源。

## 执行与结果

```json
{
  "time_range": {
    "from": "2026-11-01T00:00:00Z",
    "to": "2026-11-08T00:00:00Z"
  },
  "timezone": "Asia/Shanghai"
}
```

省略 `interval` 计算整个时间范围的聚合；加入 `interval: day`、`hour` 或 `minute` 得到趋势。整段 UV 必须直接运行整段查询，不能相加每日 UV。分组之间也可能包含相同身份，分组 UV 之和不是总体 UV。

可附加最多 3 个 `group_by` 字段、一个 `filter` 和 `limit`。实际筛选是“指标固定筛选 AND 本次筛选”，运行者不能删掉固定约束。字段仍需属于选定的已发布版本；合并后的条件树统一受现有深度、节点及参数预算约束。

复用现有异步查询引擎、去重事实、任务取消和权限复查。时间是 `[from,to)`，最多 31 天；结果默认 200 行、最多 500 行，当前没有结果续页。每项目共用最多 4 个运行任务和 32 个排队任务，没有独立指标队列绕过预算。轮询、结果及取消路径仍为 `P/queries/{query_id}` 和 `/result`。

执行重试标记保存 7 天，返回原任务的当前状态；标记有效但任务已过期或删除时返回 `QUERY_REQUEST_EXPIRED`，需新幂等键。原始机器凭据会保存在任务中，领取、执行租约及完成写入继续复查；轮换后的新凭据不能替旧任务续权。已完成结果按当前读取者的查询权限读取。

响应沿用查询结果的精度、生成时间、覆盖及水位元数据。当前覆盖仍为 `provisional`，观测到的最晚接收时间不是完整性证明，空桶不能自动解释为业务没有发生。

## 部署与存储

用新二进制显式执行迁移 `012_metrics.sql`，再启动 API 和工作进程。PostgreSQL 保存指标草稿、不可变版本、事件依赖、事务幂等记录和查询来源引用；ClickHouse 继续执行同一查询计划。迁移扩充机器授权白名单至 24 项，不给现有主体追加权限，不修改历史迁移。

公开接入 [API helper](../integrations/README.md)已允许上述指标路径，凭据仍由 owner 按实际用途签发。指标专用控制台（草稿/校验/发布/版本/运行分区）与手写 TypeScript 客户端方法已交付，生成的 Go/TypeScript 类型保持同步。控制台写操作继续携带会话 CSRF、`If-Match` 修订与幂等键；运行任务复用共享查询预算并按版本轮询结果。运行分区提供可选分组维度编辑器：事件只能从该已发布版本固定的事件中选择，属性 key 需为小写字母开头的 snake_case 标识，最多 3 个，前端在入队前复核标识与数量，边界与上文 `group_by` 契约完全一致，后端与契约无改动。

实际验证见[指标服务端记录](verification/metrics-2026-11-09.md)。功能验证不改变现有百万查询矩阵中的失败记录，也不构成千万用户容量证明。
