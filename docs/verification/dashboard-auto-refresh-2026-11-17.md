# 看板控制台自动刷新验证记录（2026-11-17）

## 问题（确定性缺口）

看板计划要求主区提供「全局时间/时区/刷新」，并明确自动刷新的安全边界：最短 60 秒、运行未结束不叠加、隐藏标签页暂停、带抖动，且不能成为既有容量承诺之外的并发来源。此前运行视图只有手动「运行看板」与服务端作业轮询，没有定时重跑能力。本切片在控制台交付有界自动刷新，**不引入任何后端新契约、定时任务或订阅通道**。

## 范围与语义决策

1. **复用显式版本运行入口**：每次自动刷新都是一次普通 `executeDashboardVersion` 调用，携带新的幂等键与当前执行体（时间窗、组件筛选、比较期、参数值），继续受项目 4 运行/32 队列预算约束，不获得额外并发。
2. **闭集间隔**：关闭、60 秒、5 分钟三档（`AUTO_REFRESH_OPTIONS`），常量 `AUTO_REFRESH_MIN_SECONDS=60`；抖动后的实际延迟恒不低于 60 秒（±10% 后取 max(60s)），避免多标签页同步形成请求脉冲。
3. **不叠加**：调度 tick 发现存在 queued/running 作业或控件冻结时不发起新运行，改为 5 秒后复查；活动运行结束后最迟 5 秒触发下一次刷新，而不是并发入队。
4. **隐藏暂停**：`document.hidden` 时 tick 直接返回且不重排；监听 `visibilitychange`，标签页重新可见后用新的抖动间隔重新计时。
5. **生命周期**：切换 `dashboard_id`/版本自动重置为关闭；卸载清理全部 timer 与监听；关闭选项立即取消待执行 tick。
6. **可观测**：控件显示下次运行的时区格式化时间与秒级倒计时（1 秒局部 ticker，不重渲染看板网格），隐藏时提示已暂停。

## 改动清单

| 文件 | 说明 |
| --- | --- |
| `dashboards/use-auto-refresh.ts` | 定时器 hook：抖动延迟、blocked 复查、visibilitychange 暂停/恢复、卸载清理 |
| `dashboards/auto-refresh-controls.tsx` | 三档选择器与下次运行倒计时展示 |
| `dashboards/run.tsx` | 状态接入、版本切换重置、blocked 取 `frozen || active`、触发 `execution.execute()` |
| `dashboards/styles.css` | 控件容器样式 |

无后端、契约、数据库改动；自动刷新仅是既有运行 API 的客户端定时调用方。

## 验证

### 静态门禁

- `pnpm typecheck`（根 tsc --noEmit）通过；`pnpm build` 成功；prettier 全量检查通过。
- 变异红绿（sha256 逐字节还原，基线 `3e842cce…`）：把 hook 的 `nextAt` 状态类型改为 `string | null` → tsc 报 TS2345（hook 内 number 不可赋值）与 TS2322（run.tsx 接收端类型不兼容）变红；Edit 还原后 `sha256sum -c` OK，typecheck 复绿。

### 语义对照（逐条对应计划约束）

| 计划约束 | 实现证据 |
| --- | --- |
| 最短 60 秒 | `AUTO_REFRESH_MIN_SECONDS` 下限 + `Math.max(60_000, …)` 抖动兜底，选项无更短档 |
| 运行未结束不叠加 | `blockedRef` 读取 `frozen || active`，blocked 时仅 5 秒复查不入队；`execute()` 自身也有 lock/active 双保险 |
| 隐藏标签页暂停 | tick 首检 `document.hidden` 返回；`visibilitychange` 可见时重新 arm |
| 抖动 | 每次 arm 重新计算 ±10% 随机抖动，多标签页不共振 |
| 不增加容量承诺 | 无新端点/新作业类型，复用同一入队事务与队列预算 |
| 切换版本关闭 | run.tsx 版本 effect 内 `setAutoRefreshSeconds(0)` |

控制台无组件级单测设施（与既有看板切片一致），交互正确性以 tsc 类型守卫（含变异红绿）、构建与本记录的语义对照为证据；定时器在真实浏览器中的行为留给既有 Playwright 全栈链路后续覆盖。

### 全量门禁

`make check PYTHON=…/venv/bin/python` 退出码 0：命名/契约/边界/监控/后端测试与 Web（typecheck、prettier、build）全绿。本切片无 Go/契约/迁移变更，PG 集成面不受影响。

## 明确未做（边界）

- 没有服务端定时报告/订阅执行；刷新只在浏览器打开且标签可见时发生。
- 不提供自定义秒数输入，间隔为固定闭集三档，防止把 UI 变成绕过 60 秒下限的途径。
- 交互配额与主体公平调度仍是独立后续能力，自动刷新不改变排队规则。
