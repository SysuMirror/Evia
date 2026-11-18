# 机器数据治理授权契约修复（machine governance grant contract fix）验证记录（2026-11-19）

## 问题（确定性缺陷）

完成审计发现：迁移 `016`/`020` 已把 `data.erasure`、`data.export` 纳入 Go 侧机器权限闭集（`access.machinePermissions` 共 26 项），存储层与 worker 授权全部按此执行，但对外 OpenAPI 的 `MachinePermission` 枚举仍停留在 24 项，`MachinePrincipal`/`MachinePrincipalInput`/`MachineIdentity` 三个 schema 的 `permissions.maxItems` 均为 24。

后果是确定性的：通过正式 HTTP API 创建携带这两个授权的机器主体时，`Boundary.Decode` 在契约校验阶段直接返回 400 `VALIDATION_FAILED`，已交付的删除/导出机器授权实际无法签发；既有集成与 transport 测试都直接构造权限绕过了创建边界，所以缺陷长期未暴露。控制台 `permissionGroups` 同样缺这两项，即使签发成功也无法在界面选择或在只读详情中显示。

## 先红后绿

1. 在 `TestMachineCredentialAdministrationContract` 增加 `create data governance grants` 用例（`["data.erasure","data.export"]` 期望 201），修复前实测 400 `VALIDATION_FAILED`、service 零调用。
2. 控制台既有 model 规格机械断言 `permissionGroups` 与契约枚举逐项相等，契约扩为 26 项后该规格立即变红（24 vs 26），证明漂移被门禁捕获。

## 修复

- `contracts/http/openapi.json`：`MachinePermission` 枚举追加 `data.erasure`、`data.export`；三处 `permissions.maxItems` 24 → 26；`verify_contracts.py` 离线校验通过。
- codegen 三步（`generate.py` → gofmt wire → `pnpm generate:api`）同步 Go wire 与 TS 类型；Go 存储侧 `len(machinePermissions)` 本就按闭集长度校验，无需改动。
- 控制台 `machine-access/model.ts` 新增「数据治理」权限组（两项，附独立敏感授权、不由其他权限推导的说明），选择与只读详情同时恢复。
- `docs/machine-access.md` 权限表补两行，创建上限 24 → 26。

## 验证

- 新增 HTTP 用例修复后 201 通过；`TestMachineCredentialAdministrationContract` 整组通过。
- 真实 PG 新增 `TestMachineGovernanceGrantsPersistAndAuthenticate`：携带双治理授权创建主体并认证，断言权限集合精确保留；`TestMachine*` 整组真实 PG 集成通过。
- 新增常驻防漂移守卫 `TestMachinePermissionContractStaysAlignedWithClosedSet`（`access` 包）：机械比对嵌入 bundle 的 `MachinePermission` 枚举与 Go `machinePermissions` 闭集，并校验三个主体 schema 的 `maxItems` 等于闭集大小；临时注入第 27 个权限实测变红（26≠27）、移除后复绿，防止此类契约滞后复发。
- Playwright model 全套 114/114（含机器权限契约对齐用例转绿），临时 `playwright.model.config.ts` 用完即删。
- `pnpm typecheck` 通过；全量 `make check` EXIT=0、零 FAIL（Go 32 包、client 51、sdk 63、console 18、生产构建）。

## 明确未做（边界）

- 不改变两权限的服务端授权语义（人类 owner-only、机器显式授权）与既有主体的不可变权限集；旧主体不会自动获得新授权。
- Python 集成助手刻意保持事件发布工作流边界，不加入 erasure/export 路径。
- 权限总数的唯一真相仍是 Go `machinePermissions` 闭集；契约与控制台通过测试机械对齐，不再手工维护计数。
