# 公开接入 helper 复合指标白名单（formula-metrics allowlist）验证记录（2026-11-18）

## 问题（确定性缺口）

复合公式指标已全链路交付（迁移 `013`/`014`，嵌套复合见 `033`；后端在 `/api/v1/projects/{project_id}/formula-metrics` 注册与原子指标同构的全套 HTTP 路由，复用同一组 `metrics.*` 权限、不新增授权项）。但公开接入资产 `integrations/tooling/activity_api.py` 的 `scoped_path` 白名单只包含 `/metrics`，机器接入方经此受限 helper 无法触达任何复合指标路径——能力在平台侧存在、在唯一公开 CLI 入口缺失。同一章节的 README 还声称「Formula metrics and direct metric references inside saved analyses or dashboards are not available yet」，与已交付事实相反。

## 范围与语义决策

1. **同构镜像，不发明新表面**：新增正则与既有 `/metrics` 条目逐字同构，仅前缀替换为 `/formula-metrics`，开放集合、资源、`/draft`、`/validations`、`/versions`、`/versions/{n}` 与 `/versions/{n}/queries` 完全一致。
2. **治理面继续排除**：`/archive`、`/unarchive` 不进白名单，与原子指标的处理保持一致（归档是 owner 治理动作，不属于发布/运行工作流），并在负例中显式钉住。
3. **零后端/契约改动**：不新增路由、权限、迁移或请求形状；helper 仍是单请求包装器，不变成自主发布状态机，不引入隐式重试或会话回退。
4. **文档同切片核销**：README 同步纠正三处过时声明——趋势粒度补上 `week`（周一起算）、单位换算改为「无自动换算，但公式内有封闭的毫秒↔秒 `convert` 节点」、保存分析与看板的不可变指标版本引用改为已交付表述，并说明 helper 白名单对 `/formula-metrics` 一对一镜像且同样排除归档动作。
5. **边界**：完整自动化发布 CLI（状态机、业务仓库接入验收）仍不在本切片；SMTP 邀请/注册、身份图、外部基础设施等阻塞项不动。

## 改动清单

| 文件 | 说明 |
| --- | --- |
| `integrations/tooling/activity_api.py` | `scoped_path` 白名单新增一条 `/formula-metrics` 同构正则 |
| `integrations/tests/test_assets.py` | 新增 `test_formula_metric_routes_preserve_scope_and_explicit_allowlist`（7 正例、9 负例） |
| `integrations/README.md` | 复合指标路径说明与三处过时能力声明核销 |

无新增打包文件，`package_assets.py` 九文件白名单不变，打包确定性测试继续通过。

## 验证

### 变异红绿

- 基线 sha256 存 `/tmp/evia-check/helper-formula-metrics/fe/SHA256SUMS`。
- 红：精确删除新增白名单行（Python 断言计数为 1，禁用 git checkout），新测试 7 个正例 subTest 全部报 `SafeError: API path outside the integration helper allowlist`（FAILED errors=7），证明正例确实由新正则放行而非被其他模式意外吸收。
- 绿：按锚点精确插回，`sha256sum -c` 两文件与基线逐字节一致；集成资产套件复跑 16/16 通过。

### 测试与门禁

- 新测试正例覆盖集合查询串、资源、草稿、校验、版本列表/详情、版本运行入队；负例覆盖 `/latest`、`versions/0`、`versions/1/sql`、`archive`、`unarchive`、跨环境查询串、伪造权限参数与跨项目 UUID。
- `python3 -m unittest discover -s integrations/tests`：16/16 通过；打包白名单与确定性字节测试在内。
- 全量 `make check` EXIT=0、零 FAIL：Go 32 包 ok、集成资产 16 项、client 49/49、sdk 63/63、console 18/18、生产构建通过。日志：`/tmp/evia-check/helper-formula-metrics/make-check.log`。

## 明确未做（边界）

- 不改后端路由、权限模型、契约或迁移；不开放归档/恢复等治理路径给该 helper。
- 不实现完整自动发布 CLI 状态机（仍需业务仓库接入验收，属 feature-coverage 行 31 的独立工作包）。
- 漏斗严格有序、身份关联图、来源系统字段分析化、热力图/会话回放与外部基础设施仍待单独拍板或外部环境。
