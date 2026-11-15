# 账号运维审计只读查询验证记录（2026-11-15）

## 问题（确定性缺口）

迁移 028 与类型化审计切片让 `account_provision_events` 记录四类运维事件
（`account_created/account_disabled/account_enabled/password_reset`），但该表只有写入路径、
没有任何读取入口。operator 无法回答「某账号何时被停用、何时改过密码」，类型化审计未形成闭环。

## 改动（只读切片，无迁移、无契约变更）

| 层 | 文件 | 说明 |
| --- | --- | --- |
| 领域 | `internal/access/provision.go` | 新增 `AuditEvent`（仅 `event_type`+UTC `occurred_at` 安全元数据）；`ProvisionStore` 接口加 `AccountAuditEvents`；服务函数做用户名归一化与 pattern 校验 |
| 持久化 | `internal/storage/postgres/provision.go` | 先按用户名解析账号（不存在即 `ACCOUNT_NOT_FOUND`），再按 `occurred_at,id` 顺序读事件，时间戳统一 `.UTC()` |
| CLI | `cmd/evia/provision.go`、`cmd/evia/main.go` | 新增 `evia account-audit` 命令；复用仅 `EVIA_DATABASE_URL`+`EVIA_ACCOUNT_USERNAME` 的运维装配；结构化 logger 输出，不回显用户名/凭据 |
| 测试 | access、cmd、postgres 三处 | 见下 |

## 关键设计

1. **只读、无副作用**：不开放 HTTP 接口，仍是数据库运维命令，与账号运维命令族同一信任边界；
   不创建会话、不改成员关系、不写任何表。
2. **未知账号 fail-closed**：先做账号存在性检查，未知用户名返回 `ACCOUNT_NOT_FOUND` 而非空列表，
   与变更类命令的 not-found 语义一致，避免把「账号不存在」误读为「无运维历史」。
3. **输出最小化**：只输出闭集事件类型与 UTC 时间、序号；无 actor 个人身份（表本身只有固定
   `actor_kind='operator'`）、无密码/哈希/凭据 ID；用户名不回显。
4. **复用既有校验**：用户名归一化（trim+lowercase）与 `usernamePattern` 与三个变更命令完全一致，
   非法输入在到达持久层前被拒。

## 验证

### 单元测试

- `internal/access`：归一化委托、非法用户名在持久化前拒绝、存储故障透传，3 个新测试；
- `cmd/evia`：`TestAccountAuditCLIOutputsStructuredSafeHistory` 验证输出含全部事件类型与
  `event_count/sequence/occurred_at` 字段且不含用户名；not-found 映射为安全 CLI 错误且无成功日志；
  非法用户名零持久化调用。路由守护测试 `TestAccountAdministrationCommandsRouteThroughDatabaseOnly`
  的命令白名单纳入 `account-audit`，自动钉住「需 EVIA_DATABASE_URL、拒绝位置参数、拼写错误仍为
  unknown command」三条不变量。

### 真实 PostgreSQL 集成（隔离 schema，`-race -tags=integration`）

`TestAccountAuditEventsReadsTypedHistory`：创建→停用→启用→幂等再启用（no-op 不写）→改密后，
`account-audit` 读回严格有序的四个事件类型；断言时间戳全部归一到 UTC 且单调不减；未知账号
`ACCOUNT_NOT_FOUND`。与上一切片的
`TestAccountProvisionEventTypesAuditFullLifecycle` 同时通过。

### 变异红绿（两轮，sha256 逐字节还原）

1. 移除 postgres 账号存在性检查并把 target 写死 → 集成测试变红
   （`audit event count = 0, want 4`），证明 target 解析是查询正确性的前提；
2. 移除 access 用户名 pattern 校验 → 非法用户名穿透，
   `TestAccountAuditEventsRejectsInvalidUsernameBeforePersistence` 变红
   （`invalid username must fail closed`）。

还原后 `internal/access`、`cmd/evia` 全部 `-race` 转绿，`gofmt`/`go vet` 干净。

### 门禁

完整 `make check` 退出码 0；命名检查无禁词。
