# 多 Part 形态回收 Headroom 对照验证记录（2026-11-14）

## 范围

[边界月份对照](retention-boundary-partition-2026-11-14.md)在「每分区单 part
（OPTIMIZE FINAL 后）」形态下证明了 headroom 对分区布局不敏感，但其验证
文档明确留下一个未覆盖项：**每分区多 part 的生产表形态**——真实高频写入
表在两次合并之间累积大量 part，一次 mutation 要重写的 part 数远多于单
part 形态，headroom 是否随之显著上升尚无证据。本切片在同一探针内引入
part 形态维度回答该问题。

无生产代码变更；改动集中在 `retention_headroom_probe.py` 与其单测。

## 交付内容

- 探针新增 `PartShape` 闭集（`merged` / `unmerged`）：merged 沿用
  OPTIMIZE FINAL 的每分区单 part；unmerged 用 5000 行/批装载（30 万行
  计划 60 个 part），并通过表级 `merge_selecting_sleep_ms=60000` 拖慢
  普通后台合并，使多数 part 在 sweep 前存活。
- 新增纯函数 `insert_batch_sizes`（确定性 part 批次规划）、Cell 的
  `part_rows`/`planned_parts`，评估器按形态分支断言 part 数。
- 单测从 41 项扩到 53 项（benchmark 门控累计 115 项）。

## 关键先红后绿：STOP MERGES 方案被否决

第一版 unmerged 用 `SYSTEM STOP MERGES` 冻结 part 数，真实库上首条
`ALTER DELETE ... mutations_sync=1` 即卡死超过 400 秒。取证确认根因：

- ClickHouse 的 mutation 本质是一种特殊 merge，**STOP MERGES 同时冻结
  了执行 mutation 的 merge 路径**，同步 mutation 因此永久等待。
- 改为 `SYSTEM START MERGES` 后，引擎在执行 mutation 前立即把全部小
  part 合并成 1 个（实测 8 part → 1），无法保留多 part 形态。

结论：在 MergeTree 上不存在「冻结 part 同时允许 mutation」的确定开关。
最终方案改用 `merge_selecting_sleep_ms=60000`（服务器上限）只拖慢普通
合并选择器、不阻塞 mutation 路径；实测 mutation 0.12 秒级正常完成。
因此 unmerged **不承诺精确 part 数**，评估器只断言「sweep 前 part 数
严格大于分区数」以证明多 part 前提成立，实际数字如实报告。该减速对
30k 行等秒级小负载无效（装载短于一个 sleep 周期，part 仍被合并），
仅适用于 30 万行级、装载跨越多个周期的目标规模。

## 对照矩阵（30 万行 / 50% 过期，两轮独立运行逐值一致）

| 单元格 | 布局 | 形态 | part 前→后 | headroom 比率 | 回收比例 |
| --- | --- | --- | --- | --- | --- |
| s300000_f50_m | segregated | merged | 2→2 | **7.2636** | 0.5001 |
| s300000_f50_u | segregated | unmerged | 6→6 | **7.5305** | 0.5001 |
| b300000_f50_m | boundary | merged | 1→1 | **7.2577** | 0.4999 |
| b300000_f50_u | boundary | unmerged | 7→1 | **7.4865** | 0.5018 |

全部单元格：mutation 6/6 同步完成、过期残留 0、两轮的比率与 part 数
逐值相同。unmerged 实际保住 6–7 个活跃 part（计划 60，多数在装载期被
减速中的合并器逐步合并）。

## 关键结论：多 part 惩罚温和，9 倍余量仍然充分

1. **headroom 对 part 数不敏感**：从每分区 1–2 part 增到 6–7 part，
   比率仅上升 0.27（segregated 7.26→7.53）、0.23（boundary 7.26→7.49），
   相对增幅约 3–4%。主导因素仍是连续 6 个同步 mutation 的 part 世代
   叠加，而非单次重写的 part 数量。
2. **9 倍工程余量覆盖多 part 形态**：全部实测单元格最大 7.53x，距 9x
   余量仍有充足安全边距，无需为高频写入的多 part 表单独加预算。
3. **mutation 会顺带合并 part**：boundary unmerged 的 part 数在 sweep
   后从 7 降到 1，说明 mutation 重写本身产生合并效果；segregated
   unmerged 维持 6 是因为幸存行分布在未被该作用域 mutation 触及的
   part 上。这是引擎真实行为，已如实记录而非假设。

## 边界与诚实声明

- 多 part 形态由 `merge_selecting_sleep_ms` 减速构造，part 数是实测值
  而非精确受控值；本矩阵得到 6–7 part，不代表生产表可能出现的几十上百
  part 的极端形态，那种规模需调整减速参数或用专用压测环境复测。
- 减速设置只作用于隔离探针库的临时表，库随 SYNC 删除而消失，不影响
  dev/基准实例的默认合并行为；探针结束零库与 metadata_dropped 残留。
- 结论在 30 万行、单节点 4GiB、单表 6 作用域下成立；part 数远超本矩阵
  时比率是否仍平稳，不能据此外推。
