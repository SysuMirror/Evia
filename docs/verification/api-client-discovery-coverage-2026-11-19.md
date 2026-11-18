# 手写 API 客户端分析发现面全覆盖（api-client discovery coverage）验证记录（2026-11-19）

## 问题（确定性缺口）

对 `contracts/http/openapi.json` 的 129 个操作做 operationId × `packages/api-client` 方法的机械审计，发现四个已注册的正式管理接口在 TS 手写客户端中没有对应方法，与 feature-coverage「手写 API 客户端已为全部正式接口提供调用方法」的声明矛盾：

- `GET /projects/{id}/analysis-capabilities`（`getAnalysisCapabilities`）
- `GET /projects/{id}/analysis-catalog`（`getAnalysisCatalog`，必带 `environment`，可选 `limit`/`cursor`）
- `POST /projects/{id}/query-validations`（`validateQuery`，会话写操作需 CSRF，不创建作业）
- `POST /projects/{id}/journeys/query`（`createJourneyQuery`，返回普通 `QueryJob`）

四者均为 `projectAuth` 会话/机器双认证的正式接口，OpenAPI schema（`AnalysisCapabilities`/`AnalysisCatalog`/`QueryValidation`/query-job）完整；机器侧早已由 `integrations/tooling/activity_api.py` 白名单覆盖，缺口仅在 TS 客户端。

## 改动（纯客户端契约补全，无语义决策）

- 新增类型别名 `AnalysisCapabilities`、`AnalysisCatalog`、`QueryValidation`（均取自已生成的 `components` 类型，不新造形状）。
- 四个方法放在既有查询方法旁，复用现有助手：GET 走 `catalogQuery` 编码分页/环境参数，POST 复用统一 `request` 的 `csrf` 选项，旅程查询返回 `QueryJob`。
- 不新增端点、不改后端、不引入控制台 UI（发现/校验能力的控制台工作区是独立产品切片，不在本次范围）。

## 验证

- 新增一条 node:test 用例：断言四方法的 URL 编码（含 `cursor=a%2B%2F%3D&limit=50` 的签名游标透传）、GET/POST 方法、`X-CSRF-Token: csrf`、请求体字节与响应类型映射。
- `pnpm typecheck`（`tsc --noEmit`）通过；`pnpm test:client` 51/51（原 50 + 新增 1），零失败。
- 全量 `make check` EXIT=0、零 FAIL（Go 32 包、client 51、sdk 63、console 18、生产构建），日志：`/tmp/` 下本轮门禁输出。

## 明确未做（边界）

- 不交付控制台发现/校验 UI：四接口的浏览器工作区接入涉及交互设计，作为独立切片。
- 不改变四端点的授权（会话成员或机器 `queries.read`/`queries.run`/`queries.raw.read`）与服务端行为。
- 采集面 `/v1/collect`、`/v1/config` 属浏览器 SDK 职责，`/machine-identity` 属 Python 机器助手职责，刻意不进管理客户端。
