# 继任属性布局物理读门控：有限热键专用列通过、Map 布局复测仍失败（2026-11-12）

执行日期：2026-11-12，UTC。本文用与 [2026-11-10 Map 门控](property-layout-reads-2026-11-10.md) 同一口径的物理读指标，评估布局计划点名的第一继任方案——**有限热键专用列（dedicated hot-key columns）**。结论先行：

- **专用列在两类工作负载、三项物理指标上全部通过门控**：数值过滤的读字节降至文档路径的 0.3%、峰值内存降至 0.5%；字符串分组读字节降至 3.7%、峰值内存降至 52.6%；SelectedMarks 仅 +1 个 granule，在显式容差内。
- **`Map(String,String)` 布局在同一探针下复测仍失败**：两类负载均多选 2.185 倍 granule，数值过滤峰值内存为基线的 13.86 倍，独立复现了 2026-11-10 的搁置结论。
- 本次证据**只是物理读门控证据**。探针是隔离实验工具，不改动任何 writer、reader、迁移或生产数据；它不构成生产容量声明、不重开 514/728 容量矩阵，也不构成 reader 切换许可。

## 测量条件

- 探针为 `tooling/benchmark/property_layout_probe.py`（纯标准库实验框架，非生产代码），在 ClickHouse 26.8.36.3 上创建一次性库 `evia_probe_layout`，测量结束执行 `DROP DATABASE ... SYNC`（实测 tmpfs 空闲由 208.61 MiB 回升至 346.35 MiB）。
- 数据集复刻 2026-11-10 门控画像：100,000 行宽文档，每份文档含 24 个约 100 字节定宽字符串属性（`segment_00`..`segment_23`）加 1 个均匀分布于 0–99 的数值属性 `metric_07`。三份逻辑完全相同的数据分别装入同库三张表，导入后 `OPTIMIZE ... FINAL` 合并为单分片：
  - `probe_legacy`：`id UInt64 + properties_json String`，权威文档列基线。
  - `probe_map`：追加 `property_layout_version UInt8 DEFAULT 1` 与 `property_tokens Map(String,String)`，键为 `p_` 前缀大写 hex（与 Go `internal/propertylayout.EncodeKey` 同算法，单测钉住 `metric_07 → p_6D65747269635F3037`、`segment_03 → p_7365676D656E745F3033`），值为逐字 JSON token。
  - `probe_dedicated`：追加两个有界热键专用列 `segment_03 String` 与 `metric_07 Int64`，其余属性仍只存在于文档列。
- 与生产相同的 `max_threads=2`；每次查询前 `SYSTEM DROP MARK CACHE`；每类查询 5 次重复，指标取自 `system.query_log` 的 `QueryFinish` 行（`read_bytes`、`ProfileEvents['SelectedMarks']`、`memory_usage`、`query_duration_ms`），取末次稳定值；query_log 异步刷新，测量后 `SYSTEM FLUSH LOGS` 并以 20 次 ×0.5s 有界轮询等待落盘。
- 门控容差显式写进探针并由单测钉住：连续指标（读字节、峰值内存）相对基线劣化不超过 2%；SelectedMarks 是离散 granule 整数，允许绝对 +2 且比率不超过 1.10——绝对容差吸收单 granule 对齐噪声，比率上限拒绝结构性多读（Map 的 2.185 倍即据此被驳回）。基线为零时候选必须为零。

## 语义等价核对（先于物理测量）

- 数值过滤 `metric_07 > 50`：三种布局计数均为 **49,000**，与解析期望 `sum(i % 100 > 50)` 一致。
- 字符串分组按 `segment_03`：以 `SELECT count() FROM (... GROUP BY ...)` 标量子查询比较 distinct group 数，三种布局均为 **100,000**（定宽值每行唯一）。
- 探针在任一布局结果不一致时直接 fail-closed 终止，不产出门控结论。

## 物理读与内存结果

数值过滤（5 次重复的稳定值）：

| 布局 | read_bytes | SelectedMarks | peak memory | 耗时 |
| --- | ---: | ---: | ---: | ---: |
| legacy 文档列 `JSONExtractInt` | 280,389,750 | 27 | 29,468,722 | 282 ms |
| map token 点查 | 588,302 | 59 | 408,316,846 | 206 ms |
| **dedicated 专用列** | **800,000** | **28** | **160,064** | **3 ms** |

字符串分组：

| 布局 | read_bytes | SelectedMarks | peak memory | 耗时 |
| --- | ---: | ---: | ---: | ---: |
| legacy 文档列 `JSONExtractString` | 280,389,750 | 27 | 52,548,338 | 321 ms |
| map token 分组 | 10,500,000 | 59 | 39,358,226 | 218 ms |
| **dedicated 专用列** | **10,300,000** | **28** | **27,649,812** | **42 ms** |

门控裁决（证据 JSON 原样记录每条比率与边界）：

| 候选 | 工作负载 | read_bytes 比率 | marks 比率 | 内存比率 | 裁决 |
| --- | --- | ---: | ---: | ---: | --- |
| map | numeric_filter | 0.0021 | 2.1852 | 13.8559 | **FAILED**（marks、内存越界） |
| dedicated | numeric_filter | 0.0029 | 1.0370 | 0.0054 | **PASSED** |
| map | string_group | 0.0374 | 2.1852 | 0.7490 | **FAILED**（marks 越界） |
| dedicated | string_group | 0.0367 | 1.0370 | 0.5262 | **PASSED** |

### 发现

1. **专用列把属性读取从「整份宽文档逐 granule 解析」变为「只读目标列」**。数值过滤几乎不再触碰文档列：读字节 0.3%、内存 0.5%、耗时 282ms→3ms。字符串分组仍需聚合全部 distinct 值，因此内存只降到 52.6%，但读字节降至 3.7%、耗时 321ms→42ms。
2. **Map 的失败是结构性的而非噪声**：两个负载 marks 都是 27→59（2.185 倍），与 2026-11-10 观察到的「Map 存为并行键/值数组流，点查仍按 granule 读整条数据流」一致；数值过滤内存 13.86 倍则来自 Map 查找与类型转换的运行时开销。Map 在字符串分组上读字节本身不差（0.0374），但 marks 结构性越界仍使门控失败。
3. **dedicated 的 27→28 marks 是单 granule 对齐差异**，绝对差 1、比率 1.037，落在探针显式容差内；连续指标无一劣化，因此不是结构性退化。

## 静态存储（system.parts / parts_columns，各 100,000 行活跃分片）

| 表 | 磁盘占用 | 列数据压缩后 | 未压缩 |
| --- | ---: | ---: | ---: |
| probe_legacy | 22.95 MiB | 22.94 MiB | 268.55 MiB |
| probe_dedicated | 23.46 MiB | 23.44 MiB | 279.61 MiB |
| probe_map | 37.97 MiB | 37.97 MiB | 593.45 MiB |

专用列只比基线多占 0.51 MiB（+2.2%）：`metric_07` 压缩后 7.29 KiB、`segment_03` 508.30 KiB；Map 布局多占 15.02 MiB（+65.4%）。本画像文档列压缩比约 11.9 倍，与 2026-11-10 的约 31 倍不同，两次实验的绝对体积不具横向可比性，结论只依赖同次实验内的相对门控。

## 局限与诚实边界

1. **合成数据画像偏置**：键名固定、字符串定宽且高度重复，对单个 String 文档列的压缩极其有利；真实低压缩比、长尾键分布下差距可能收窄或变化，结论不外推。
2. **规模与链路不完整**：单库 10 万行、`OPTIMIZE FINAL` 后单分片，不是百万行容量矩阵；未含 `event_facts` 视图 argMin 聚合、并发查询、持续混合读写与临时盘配额场景。514/728 的失败记录仍然有效，本次不推翻。
3. **只物化了 25 个属性中的 2 个热键**。生产化所必需的热键识别机制、有界列数上限、schema 演进与列回收规则均未设计；无界加列会把宽表推向反向极端。
4. **没有 writer/reader/迁移集成方案**：专用列与权威文档的同事务原子写入、旧 parts/旧 writer 的回退读取、有界 backfill、类型化列上的词法无损语义（数值列无法保留 `1e2`、`-0.0` 等原始 token，本探针数值均为整数）都尚未实现，也未复用 18 形态等价矩阵验证。
5. 因此本文的准确含义是：**继任布局的第一候选已通过布局计划要求的物理读门控，取得了进入生产化设计的资格**；在上述集成与百万行矩阵完成前，不得声明容量问题已解决或切换生产读取路径。Map 布局继续搁置，006/007 schema 默认值维持版本 0 行为不变。

## 复现方式

```bash
export EVIA_CH_USERNAME=evia EVIA_CH_PASSWORD='<clickhouse 口令>'
python3 tooling/benchmark/property_layout_probe.py run \
  --database evia_probe_layout --rows 100000 --repeats 5
```

探针默认测完即删库；`--keep` 可保留库供人工核查。纯逻辑部分（数据生成、三布局 DDL、token 派生、标识符安全、门控裁决）由 `tooling/benchmark/test_property_layout_probe.py` 的 20 项单测钉住，经 `benchmark-tools-test` 纳入 `make check`，无需 ClickHouse 即可运行。本次门控证据 JSON 留存于执行机 `/tmp/probe-100k.json`。
