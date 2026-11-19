# 主体类别读模型守卫（principals.kind / ProjectAuditEntry.actor_kind）验证记录（2026-11-19）

## 问题（确定性缺口）

`ProjectAuditEntry.actor_kind` 枚举两值 `human/machine` 长期没有机械对齐守卫。该词汇的真实归属是 `principals.kind`：迁移 010 建表时以 `CHECK(kind IN ('human','machine'))` 约束，账户触发器为新账户写 `human`、机器签发 SQL 写 `machine`；Go 侧从不写该列，审计读取经 `audit_entries e JOIN principals p ON p.id=e.actor_id` 把 `p.kind` 扫描进 `access.AuditEntry.ActorKind`，再由边界透传为 wire 的 `ProjectAuditEntry.actor_kind`。测试侧存在 5 处裸字符串比较，词汇漂移不会在构建期暴露。

## 方案（纯增量、行为不变）

1. 新增 [principal_kind_policy.go](../../backend/internal/access/principal_kind_policy.go)：`PrincipalKindHuman/PrincipalKindMachine` 常量与 `PrincipalKinds` 切片，作为 Go 侧单一真相源；
2. 新增 [principal_kind_contract_test.go](../../backend/internal/access/principal_kind_contract_test.go)：`TestPrincipalKindVocabularyContract` 断言 `ProjectAuditEntry.actor_kind` enum 与 `PrincipalKinds` **集合相等**（两值均经读模型可观测，且存储 CHECK 恰好同值，不是子集关系）；
3. 迁移 4 个测试文件共 5 处裸比较到 `access.PrincipalKind*` 常量（control 审计夹具、postgres 审计/机器/开户集成测试）。

## 刻意保留（边界，三类相邻词汇不混入）

- raw SQL：机器签发的 `INSERT INTO principals(id,kind) VALUES($1,'machine')`、两处 `p.kind='machine'` JOIN 条件、010 触发器与 CHECK 约束——字符串活在查询文本/迁移内，由 CHECK 兜底，与既有 SQL 边界选择一致；
- `access.Project.Role = "machine"`（access.go:189 及对应测试）：机器身份访问项目的**内部角色哨兵**，不经任何 wire 契约序列化（wire.ProjectMember.Role 来自成员记录的 owner/editor/viewer 三值，已由 #2 守卫覆盖），属另一词汇；
- `account_provision_events.actor_kind='operator'`：独立表、独立 CHECK（011，只允许 operator），不在 ProjectAuditEntry 契约内；
- `UpdateProjectSettings(..., "machine", ...)` 中的字符串是**幂等键**测试值，与主体类别无关。

## 守卫有效性取证（变异红绿）

基线 sha256（policy/contract 两文件）存 `/tmp/evia-check/principal-kind/sha256-baseline.txt`，原件备份于 `baseline/`。

- 变异 A（闭集单边扩项）：向 `PrincipalKinds` 注入 `"drift_probe"` → 长度断言报 `principal kind drift: contract [human machine] != Go [drift_probe human machine]` 变红；
- 变异 B（常量值漂移、长度不变）：把 `PrincipalKindMachine` 值改为 `"drift_probe"` → 逐元素断言报 `principal kind drift: contract [human machine] != Go [drift_probe human]` 变红；
- 两条断言分支独立证有效；备份还原后 `sha256sum -c` OK，`go test ./internal/access/` 复绿，`grep drift_probe internal/access` 零残留。

## 验证

- gofmt 零差异；`go build ./...` 通过；access 包全量单测通过；
- 全量 `make check` EXIT=0、零 FAIL：Go 33 包 ok（`go test -race ./...`）、api-client 51、browser-sdk 63，命名/契约/边界/prettier/typecheck/生产构建全过。日志：`/tmp/evia-check/principal-kind/make-check.log`；
- 真实 PostgreSQL 回归：专项 `TestAudit*/TestMachine*/TestProvision*` 共 20+ 测显式 PASS（含 `TestAuditLogMachineOperationSafeAttributionAndNoReadAudit` 的 machine 归属断言、机器签发后 principals.kind 校验）；整包 `go test -race -tags integration -skip TestFailoverBaselineWrite ./internal/storage/postgres` → ok 246.7s、零 FAIL。日志：`/tmp/evia-check/principal-kind/pg-integration.log`。`TestFailoverBaselineWrite` 按既有环境门控跳过。

## 明确未做（边界）

- 不改动 SQL、触发器、CHECK 与任何运行时行为；
- 不把内部 Role 哨兵、provision operator、幂等键纳入本词汇；
- 不引入 `ValidPrincipalKind` 助手：Go 侧无入站校验调用点（kind 由存储侧写入并由 CHECK 约束），避免无人调用的代码。
