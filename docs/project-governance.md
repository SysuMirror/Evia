# 项目治理服务端

本模块实现成员管理、项目设置和审计查询。七个管理接口已接入真实 Go 运行时，使用迁移 `011_project_governance.sql`。服务端、生成的接口类型与项目治理控制台均已交付：所有者可在「项目治理」工作区维护成员与设置并查询审计，编辑者和只读成员只看到只读的项目设置分区。

## 权限与账号准备

这些接口只接受人类管理会话。浏览器写 key、服务端采集凭据和机器 Bearer 都不能使用；混合 Cookie/Bearer 请求不会回退到会话。写操作继续要求当前 CSRF token 和合法 Origin。

| 操作 | owner | editor | viewer |
| --- | --- | --- | --- |
| 读取项目设置 | 允许 | 允许 | 允许 |
| 修改项目设置 | 允许 | 拒绝 | 拒绝 |
| 列出、添加、修改和移除成员 | 允许 | 拒绝 | 拒绝 |
| 读取项目审计 | 允许 | 拒绝 | 拒绝 |

所有操作都在持久化事务内重新检查当前启用账号及项目角色，不能用先前读取的权限、游标或幂等响应绕过撤权。项目 DTO 增加 `settings.read`，owner 另有 `settings.write`、`members.manage` 和 `audit.read`；这些权限名称是人类界面能力，不是新增机器 scope。

新的人类账号由部署运维人员通过 `evia account-create` 创建，流程见[账号运维](runbooks/accounts.md)。该命令需要数据库运维访问和私有环境变量，不是项目 owner 可以调用的全局账号管理 API。它不登录账号，也不分配项目。随后项目 owner 用已存在的账号名称添加成员。

## 接口

令 `P = /api/v1/projects/{project_id}`。响应是资源本身，错误沿用 `{code,message,request_id,details?}`。所有 ID 是字符串；成员/设置修订为 JavaScript 安全整数。权威请求和响应见 [OpenAPI](../contracts/http/openapi.json)。

| 方法与路径 | 输入与返回 |
| --- | --- |
| `GET P/members` | 可选 `limit`、`cursor`；返回 `{items,next_cursor,revision}` 和 `ETag` |
| `POST P/members` | `{username,role}`；201 `{member,revision}` 和 `ETag` |
| `PATCH P/members/{user_id}` | `{role}`；200 `{member,revision}` 和 `ETag` |
| `DELETE P/members/{user_id}` | 无正文；200 被移除成员的操作快照 `{member,revision}` 和 `ETag` |
| `GET P/settings` | 无查询参数；返回 `{project_id,name,timezone,revision}` 和 `ETag` |
| `PATCH P/settings` | 完整 `{name,timezone}`；返回更新后的设置和 `ETag` |
| `GET P/audit-logs` | 必需 `from`、`to`，可选 `action`、`actor_id`、`limit`、`cursor`；返回 `{items,next_cursor}` |

所有 POST/PATCH/DELETE 都必须带单一 `Idempotency-Key` 和带双引号的 `If-Match`，以及会话 `X-CSRF-Token`。例如 `If-Match: "3"`。幂等键是 1–128 个可见 ASCII 字符。未知/重复查询参数、未知 JSON 字段、重复 JSON key 和尾随 JSON 都拒绝。成员 DELETE 不接受正文。

### 成员与并发

角色仅有 `owner`、`editor` 和 `viewer`。成员包含 `user_id`、`username`、`display_name`、`role` 和 `disabled`。接口接收符合账号字符规则的名称并转小写；添加只接受已经存在且启用的账号，不提供全局账号搜索。一个项目最多 1,000 名现有成员。

`membership_revision` 从 1 开始，每次新接受的添加、角色替换或移除递增，包括角色未改变的显式操作。它属于整个项目的成员集合，删除后重新添加也不会恢复旧修订。GET 成员按用户 UUID 升序排列，默认 50、最多 100 条；签名游标绑定读取者、项目和成员修订。成员发生变化后继续旧分页返回 `MEMBERSHIP_REVISION_CONFLICT`，应从第一页重新读取。

最后一名启用的 owner 不能被移除或降级，返回 `LAST_OWNER_REQUIRED`。可以先添加另一名启用 owner，再进行转移；并发 owner 操作仍检查当前角色和整体修订。已停用账号不能加入项目或被提升为 owner。这里保护的是平台管理操作，不能把直接修改数据库描述成受此 API 保证约束。

成员事务按 UUID 顺序取得调用者/目标成员锁，再取得项目锁，遵循现有业务操作的锁顺序。它不会在持有项目锁后再等待目标成员的写锁。成员更新、修订、管理审计和幂等快照在一个事务中提交，失败全部回滚。

成员移除不删除账号、发布资源或项目结果；该账号可以仍属于其他项目。查询工作进程沿用当前成员/角色检查：降为 viewer 后不能继续原始明细查询，移除后不能继续项目查询。已持有授权锁的业务事务可能先完成；撤权不声称抹去已完成写入，也不删除其他授权成员可读取的结果。

### 设置与重试

`settings_revision` 独立于成员修订。名称为 1–100 个 Unicode 字符，时区需为可加载的 IANA 名称且不能使用 `Local`。时区是未来操作使用的项目默认值，设置更新不改写历史事件、已发布分析/看板，或已提交查询中的显式时间范围与时区。

每次新的设置操作递增修订，内容完全相同也递增。并发旧修订返回 `PROJECT_SETTINGS_REVISION_CONFLICT`。响应丢失时，保留原始正文、修订和幂等键重试，服务器返回原接受操作的快照。同键异内容返回 `IDEMPOTENCY_CONFLICT`。

成员和设置的重试都先检查当前权限，再读取幂等记录。自己降级/退出项目后，旧 owner 请求不能通过重试恢复权限。幂等返回的修订是原操作修订，可能落后于当前状态；继续编辑前重新 GET，不能把历史快照当作最新资源。成员移除响应中的 `member` 是操作记录，不表示成员仍然存在。

### 审计分页

`from`、`to` 使用 RFC3339 时间，范围为半开区间 `[from,to)`，最大 31 天。`action` 是最长 128 字符的精确操作名，`actor_id` 是精确主体 UUID。默认 50、最多 100 条，按 `(occurred_at,id)` 倒序。

每条结果只包含 `id`、`project_id`、`actor_id`、`actor_kind`、`credential_id`、`action`、`resource_key`、`occurred_at`。审计 bigint ID 以十进制字符串表示。机器操作可以返回对应凭据 ID，不返回凭据内容、请求正文、密码或业务事件。人类操作的 `credential_id` 为 null；运维账号创建保存在独立 `account_provision_events` 中，不冒用人类主体，也不出现在项目审计接口中。

游标签名绑定读取者、项目、规范化的精确时间/操作/主体条件，不能换筛选条件后继续使用。数据库时间精度为微秒，存储查询对纳秒边界做向上取整，保持半开区间比较的真实语义。同一时间戳使用 bigint ID 消除排序歧义。

分页不锁定跨页事务快照，尚未提交的较早操作可能稍后才可见。每次读取重新授权。读取本身不写审计，避免分页不断产生自己的新条目。查询复用现有项目/时间索引；本轮没有大规模审计检索的容量结论。

## 迁移与验证

使用新二进制显式执行 `evia migrate` 至 `011`，再启动 API 和工作进程。迁移新增两个项目修订、共享治理幂等记录和运维账号创建审计，不改写历史事件或已发布定义。没有自动启动迁移或回滚命令；生产升级仍需独立备份、锁等待评估和恢复演练。

本地真实接口验证：

```sh
python tooling/verification/browser_runtime.py run /path/to/private-synthetic-runtime.env -- \
  python tooling/verification/governance_smoke.py --binary /path/to/evia
```

只支持 loopback API，使用专用合成数据库；会通过运维 CLI 创建三个合成账号，通过 API 创建项目并检查 44 个请求，不清空现有数据。账号与项目保留在专用验证环境中，用户凭据不会进入输出。该链已纳入 CI，详见[本轮验证](verification/project-governance-2026-11-09.md)。

账号恢复/停用、邀请与注册、机器权限变更、配额管理和治理记录生命周期仍待交付。项目治理控制台的成员、设置与审计分区本轮已交付，但数据生命周期、高级分析或生产容量仍未由本模块完成。
