# 账号运维类型化审计事件验证记录（2026-11-15）

## 问题（确定性缺陷）

迁移 011 建立的 `account_provision_events` 表只有 `actor_kind/target_id/occurred_at`，没有事件
类型列，所有行结构同构；且写入点只有 `ProvisionAccount` 一处。`SetAccountDisabled`（停用/启用）
与 `ResetAccountPassword`（重置密码）在同一咨询锁事务内改了账号状态或凭据，却不留下任何审计
行——账号锁定与凭据更换这两类高风险运维操作无留痕，无法在数据库侧回答「某账号何时被谁停用、
何时改过密码」。

## 改动

### 迁移 028（`028_account_provision_event_types.sql`）

- `ALTER TABLE ... ADD COLUMN event_type text NOT NULL DEFAULT 'account_created'`：存量行在历史上
  全部由 `ProvisionAccount` 写入（改动前唯一写入点），回填 `account_created` 可证精确，无猜测。
- 追加 CHECK 闭集：`account_created/account_disabled/account_enabled/password_reset`。
- 回填后 `DROP DEFAULT`：后续插入必须显式给类型，未带类型的插入在数据库侧直接失败，杜绝把新
  操作静默误标为创建。

### 存储层（`internal/storage/postgres/provision.go`）

- 新增四个包内常量镜像数据库闭集，SQL 不再内联裸字符串。
- `ProvisionAccount`：显式写 `account_created`，仍与账号插入同一事务。
- `SetAccountDisabled`：仅在 `user.Disabled != disabled` 的真实状态转换时写
  `account_disabled`/`account_enabled`；幂等重复调用（停用已停用账号、启用已启用账号）不产生
  无状态变化的噪声行，与既有 last-enabled 保护的 transition 语义一致。
- `ResetAccountPassword`：每次重置都真实替换哈希，故无条件写 `password_reset`，与哈希替换、
  会话吊销在同一事务，审计写入失败则整体回滚。

## 验证

### 真实 PostgreSQL 集成（隔离 schema，`-tags=integration -race`）

新增 `TestAccountProvisionEventTypesAuditFullLifecycle`（并入已跟踪的
`provision_integration_test.go`，未新建 Go 文件），单测内实证：

1. 迁移 028 经 `go:embed` 被 `Migrate` 正常加载并在真实 PG 应用成功；
2. 全生命周期事件序列严格为
   `account_created → account_disabled → account_enabled → password_reset`，按
   `occurred_at,id` 排序；
3. 大小写归一化后的幂等再停用、再启用不新增事件行；
4. 闭集外类型（`account_deleted`）被 CHECK 拒绝；省略 `event_type` 的插入因无默认值被拒绝；
5. 建 `BEFORE INSERT` 触发器模拟审计写入失败：改密与停用均整体失败，密码哈希与 `disabled`
   状态保持原值，证明审计与状态变更同事务原子提交。

既有 6 个运维集成测试（含并发去重、审计回滚、last-enabled 保护、会话即时吊销/恢复）全部保持
通过。

### 变异红绿取证

- 变异 1：把转换守卫 `if user.Disabled != disabled` 改为恒真 → 新用例如期变红
  `audit trail = [account_created account_disabled account_disabled], want [...]`；
- 变异 2：删除改密审计插入 → 新用例如期变红， trail 停在 `account_enabled`、缺
  `password_reset`，精准复现本切片修复的原生产缺陷；
- 两次变异均以 sha256 校验逐字节还原，还原后全包集成测试转绿。

### 门禁

`go build ./...`、`go vet`、`gofmt` 干净；`internal/access` 与 `internal/storage/postgres`
普通单元测试 `-race` 通过；完整 `make check` 退出码 0；命名检查无禁词。
