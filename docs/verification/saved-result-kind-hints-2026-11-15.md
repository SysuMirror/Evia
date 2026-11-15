# 保存分析结果页脚按查询类型区分提示验证记录（2026-11-15）

## 问题

保存分析运行结果表格下方的说明文字是 trend 专属的「缺失时间桶未补零，各桶去重数不能相加为
整段去重数」，但该组件被所有已保存查询类型（含看板组件）复用。对 funnel/retention/segment/
path/attribution 而言，这句话描述的「时间桶」「去重数相加」语义并不存在，属于误导性文案。

## 改动（纯前端，无后端/契约/迁移）

`apps/console/src/saved-analysis-run/result-presentation.tsx`：

- 抽出纯函数 `savedResultHint(kind)`，行数统计行保持通用，仅说明文字按 `query.kind` 切换。
- trend 完整保留原文案；其余五类使用与各自后端评估器一致的语义：
  - funnel：转化率相对首步、步骤有序、未到达不补零；
  - retention：0 期留存率恒为 1.0、队列按起始周期划分；
  - segment：主体按最早分群事件标量值唯一归组、缺失值归固定组；
  - path：因果序、深度上限、不足深度的主体不参与；
  - attribution：主体只归因一次、无触点归 `__direct__`、占比按转化主体数。
- 默认分支对未知 kind 给出中性的「无匹配不等同数值零」，避免再出现错配文案。

## 正确性核对

每条提示均对照后端实现确认：`funnel.go` 的 `conversion_rate=totals[i]/totals[0]`、
`retention.go` 的 0 期 1.0 与队列划分、`segment.go` 的唯一归组与缺失组、`path.go` 的深度排除、
`attribution.go` 的单次归因与 `__direct__` 回退。trend 分支字符串未改动，行为保持。

## 验证

`pnpm typecheck` 零错误、`pnpm build` 生产构建成功、prettier 格式检查通过；完整 `make check`
退出码 0（32 个 Go 包 `-race` 全 ok、零 FAIL，前端各检查与构建全过）。组件被保存分析运行页与
看板运行（`dashboards/run.tsx`）共同复用，本修复两处同时生效。
