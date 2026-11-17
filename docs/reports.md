# 管理分析报告（Managed analysis reports）

报告是按阅读顺序编排的文档，把多个已发布的保存分析不可变版本固定为一份可审计的编译产物。它与看板刻意区分：看板是带空间坐标的实时版面（`x/y/width/height` 部件），报告没有布局，只有 1..50 的 `reading_order` 章节序列。每个章节通过关系外键钉住 `saved_analysis_versions` 的具体版本，草稿、未发布或已删除的分析版本无法被引用。

## 领域与持久化

- 领域内核位于 `backend/internal/reports`：标题 1..128 字符、描述上限 2000、章节 1..50、阅读顺序唯一、章节标题 1..128、同一报告不得重复引用同一分析版本。
- 迁移 `025_reports.sql` 建立三张表：
  - `reports`：按 `(project_id, report_id)` 主键保存文档定义 JSON 与乐观修订 `revision`。
  - `report_sections`：章节关系化存储，`UNIQUE(project_id, report_id, reading_order)` 保证顺序唯一；外键 `(project_id, environment, analysis_id, analysis_version)` 引用 `saved_analysis_versions` 的同名唯一约束，随报告 `ON DELETE CASCADE`。
  - `report_mutations`：`(project_id, actor_id, idempotency_key)` 幂等账本，保存请求 sha256 摘要与首次接受的响应快照。
- 外键违例（23503）在同一事务内转换为 `REPORT_REFERENCE_INVALID`，章节顺序/标识冲突（23505）转换为 `REPORT_SECTION_CONFLICT`，不会泄露底层数据库错误。

## 治理边界

报告是纯人类治理资源，不接受机器主体：

- 机器凭据在服务层与存储事务两层都被拒绝（`MACHINE_FORBIDDEN`），未来传输层变化无法绕过该规则。
- 项目成员（viewer）可读；只有 owner/editor 可写，viewer 写入返回 `REPORT_FORBIDDEN`。
- 非成员看到统一的 `PROJECT_NOT_FOUND`，不暴露项目存在性。
- 每次创建、更新、删除都写 `audit_entries`（`report.created` / `report.updated` / `report.deleted`）。

## 修订与幂等语义

- 创建必须携带 `If-Match: "0"`（期望缺席）；更新必须携带正修订。存储层在 `SELECT ... FOR UPDATE` 后比较当前修订，不匹配返回 `REPORT_REVISION_CONFLICT`，成功更新把 `revision` 加一。
- 创建与更新共享同一幂等账本：同键同请求体重放返回首次接受的快照且不重复增加修订；同键不同请求体返回 `IDEMPOTENCY_CONFLICT`。因此网络不确定时可以安全重试 PATCH。
- 删除为 204 无内容响应，章节随报告级联删除；对已删除资源再次删除返回 `REPORT_NOT_FOUND`。

## HTTP 表面

契约见 `contracts/http/openapi.json`，运行时由 `control.ReportsAPI` 经 `projectAuth` 挂载（`backend/internal/transport/control/reports.go`）：

| 方法与路径 | 说明 |
| --- | --- |
| `GET P/reports?environment=&cursor=&limit=&q=` | 键集分页列表，游标按 `reports:用户:项目:环境:搜索词` 作用域编码，可选 `q` 标题搜索 |
| `POST P/reports` | 创建，`If-Match: "0"` + `Idempotency-Key` + CSRF，201 并回 `ETag` |
| `GET P/reports/{report_id}` | 读取单报告与当前修订 ETag |
| `PATCH P/reports/{report_id}` | 整体替换定义与章节，正修订 + 幂等键，200 |
| `DELETE P/reports/{report_id}` | 级联删除，204 |

`P = /api/v1/projects/{project_id}`。错误响应统一使用 Problem 契约，覆盖 400/401/403/404/409/405/413/415/429/500/503。TS 客户端在 `@evia/api-client` 提供五个同名方法，控制台「分析报告」工作区（`apps/console/src/reports/`）支持章节编排、修订编辑、删除确认、标题搜索与只读模式。

## 标题搜索

报告列表接受可选 `q` 查询参数，按大小写不敏感的标题子串过滤，与看板/保存分析/指标的 `q` 契约同构：边界去首尾空白，超过 128 rune 返回 `INVALID_REPORT_INPUT`，省略或空白即不过滤。reports 是独立的精简领域包（只依赖 access/fault），刻意不反向依赖 reporting bounded context，因此归一化与 LIKE 转义由包内 `NormalizeTitleSearch`/`EscapeLikePattern` 本地实现，而不是复用 reporting 包的共享内核。服务端转义 `\`、`%`、`_` 并在存储侧声明 `ESCAPE '\'`，元字符按字面量匹配；请求文本只作为绑定参数、永不拼接进 SQL。搜索与环境谓词合取且不跨环境，归一化词进入签名游标作用域（`reports:actor:project:env:term`），跨词分页返回 `INVALID_CURSOR`；查询参数走 `governanceQuery` 闭集白名单（`environment`/`limit`/`cursor`/`q`），未知参数 fail-closed。控制台提供 300ms 防抖搜索框、镜像 128 rune 钳制、空白省略 `q` 并在换词时回到第一页。

## 证据与测试

- 领域单测 `internal/reports/reports_test.go` 覆盖全部边界与服务端口委托；`search_test.go` 覆盖搜索词 trim、128 rune 拒绝与 `\`/`%`/`_` 字面量转义。
- 真实 PostgreSQL 集成 `reports_integration_test.go`：外键钉版与非法引用、环境不匹配、幂等重放/冲突、乐观修订、章节替换、键集分页、级联删除、viewer/outsider/machine 权限与审计计数。联调中捕获并修复了一处真实缺陷：`Store.Update` 曾缺少 `tx.Commit`，事务靠 defer 静默回滚，表现为接口返回成功但修订与审计全部丢失。
- `report_search_integration_test.go` 覆盖标题子串过滤：中文/拉丁大小写不敏感命中、`%`/`_` 仅按字面量匹配、无匹配返回空页、staging 环境隔离不跨环境。
- HTTP 处理器测试覆盖请求映射、创建/更新修订规则、游标分页、204 删除与故障码传播，并断言搜索词经 trim 到达服务、游标 scope 绑定搜索词、未知参数与超长词 400。
- 全栈冒烟（真实会话 + 真实已发布分析）20 个断言全部通过：201 创建、`REPORT_REFERENCE_INVALID` 外键拒绝、幂等重放与 409、`REPORT_REVISION_CONFLICT`、PATCH 重放不增修订、405/404/401/400/204，并在数据库直接核验三类审计各一条、删除后报告与章节均为零残留。

## 已知限制

- 章节只能引用已发布的保存分析，不支持复合公式指标或看板直接引用。
- 报告是环境内资源，跨环境引用分析版本会被复向外键拒绝。
- 报告本身不执行查询，只固定阅读顺序与版本指针；实时数据仍需通过保存分析运行接口获取。
