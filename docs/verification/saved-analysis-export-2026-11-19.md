# 保存分析不可变版本导出（saved-analysis version export）验证记录（2026-11-19）

## 问题（确定性缺口）

`docs/saved-analyses.md` 明确记录「exports of saved analyses remain planned」：既有 `POST /projects/{id}/exports` 只接受内联 `analysis.Request`，无法把已发布保存分析的不可变版本直接物化为导出产物；且导出 worker（`exportWorkerRuntime`）装配的是原始 ClickHouse 引擎，即便请求带内部 `formula_ref` 标记也无法路由复合公式 DAG。本切片打通「不可变版本 → 导出作业 → 一次性下载」的完整链路。

## 设计决策

- **路由形态**：新增 `POST /api/v1/projects/{project_id}/saved-analyses/{analysis_id}/versions/{version}/exports`，与既有 `.../versions/{version}/queries` 运行端点同级；调用方只提交 `name/format/time_range/timezone/artifact_expiry_at`，有界查询由服务端按版本快照绑定，不接受内联 query 体。
- **授权模型**：沿用导出权威（人类会话 owner-only、机器需显式 `data.export`），不新增权限；pin 校验内部复用 `metrics.read`。与内联导出共享 `export_jobs` 的每主体幂等空间，hash 不一致仍返回 `IDEMPOTENCY_CONFLICT`。
- **不可变语义**：单事务内项目锁 → 读快照 → 导出权威 → 归档守卫（409 `SAVED_ANALYSIS_ARCHIVED`）→ pin 重验（409 `METRIC_PIN_CONFLICT`）→ 绑定请求 → 共享插入助手，重试不产生重复作业。
- **复合路由**：复合 pin 构造与运行路径完全相同的内部 `formula_ref` marker（31 天预算、IANA 时区闭集）；导出 worker 引擎改包 `reporting.NewCompositeFormulaEngine`，普通请求透传不变，与 query worker 池同一套 DAG 展开。
- **来源见证**：迁移 041 仅加两个可空列，`MATCH SIMPLE` 外键跳过历史内联行，soundness CHECK 要求双列同空同实，无 backfill。

## 改动清单

### 契约（`contracts/http/openapi.json`）
- 新 schema `SavedAnalysisExportInput`（name 1..200、format 闭集默认 json、time_range/timezone 必填、artifact_expiry_at 可空）与新路径 `createSavedAnalysisExportJob`（201/200 返 `ExportJob`，错误集含 404/409）。
- `ExportJob` 增加可空 `source_analysis_id`（uuid）与 `source_analysis_version`（int≥1）；`tooling/verify_contracts.py` 离线校验通过。

### 迁移 `041_export_saved_analysis_source.sql`（无 backfill）
- `export_jobs` 加 `source_analysis_id uuid`、`source_analysis_version bigint`；外键 `(project_id, source_analysis_id, source_analysis_version) → saved_analysis_versions(project_id, analysis_id, version) MATCH SIMPLE`；`export_jobs_source_sound` CHECK 双列同空同实。

### Go 领域与存储
- `exporting`：新增 `SavedAnalysisSource`、`SavedAnalysisExportInput`、`ValidateSavedAnalysisExportInput`（域边界重复 HTTP 契约），`Job/Input` 携带 `Source`，`Store` 接口与 `Service` 增加 `CreateSavedAnalysisExportJob`。
- `postgres/export_jobs.go`：列清单/扫描扩两列并组装 `Source`；抽取共享 `createExportJobTx`（幂等查找 + 插入 + 审计），INSERT 携来源列，内联导出为 NULL。
- `postgres/export_saved_analysis.go`（新文件）：`bindSavedAnalysisExportRequest` 镜像 `EnqueueSavedAnalysisVersion` 的复合 marker/模板 Bind 双分支；`CreateSavedAnalysisExportJob` 串联项目锁、快照、导出权威、归档守卫、`verifySavedMetricPinTx`、UTC 归一与共享插入。
- `transport/control/export.go`：新路由与 `createFromSavedAnalysis` 处理器，`exportJobWire` 输出来源见证。
- `cmd/evia/runtime.go`：导出 worker 引擎包 `CompositeFormulaEngine`。

### 客户端与控制台
- codegen 三步（`generate.py` → gofmt wire → `pnpm generate:api`）；api-client 新增 `createSavedAnalysisExportJob` 方法与类型，新增一条方法级测试（URL/方法/幂等头/CSRF/来源字段）。
- 运行视图新增「导出此版本」按钮（按 `data.export` 门控）与 `SavedExportDialog`（名称/格式、复用运行页时间范围预算、401/403 走拒绝路径、成功后失效导出缓存并跳转导出工作区）；导出列表新增「来源」列（内联查询 / 保存分析 vN）。

## 验证

### 真实 PostgreSQL 集成（隔离 schema + 全量迁移到 041）
新增 `export_saved_analysis_integration_test.go` 四组用例：
1. `TestSavedAnalysisExportBindsSnapshotAndReplays`：内联版本导出 CSV，断言来源见证、绑定环境/时间窗、无 formula marker、幂等重放不新增、异 hash 409、Get/Claim 保留见证；直接 SQL 写半套来源被 soundness CHECK 拒绝（23514）。
2. `TestSavedAnalysisExportAuthorityArchiveAndVersionGuards`：viewer/outsider 拒绝、未知版本拒绝、归档后 409 `SAVED_ANALYSIS_ARCHIVED`。
3. `TestSavedAnalysisExportAtomicPinDriftFailsClosed`：经 `session_replication_role=replica` 单连接绕过不可变触发器篡改 `metric_versions.definition_hash` 后 409 `METRIC_PIN_CONFLICT`。
4. `TestSavedAnalysisExportCompositeMarkerBudgetsAndDrift`：复合导出持久化 `formula_ref` 标记（表内 JSON 与 claim 双断言）、32 天窗口 `QUERY_COST_EXCEEDED`、坏时区 `INVALID_TIMEZONE`、`formula_metric_versions` 漂移同样 fail-closed。

整包回归：除既有环境门控 `TestFailoverBaselineWrite`（需独立 `EVIA_TEST_PG_FAILOVER_DSN`，干净基线同样失败）外全部通过（约 106s），证明 041 在全量迁移链上无回归。

### 变异红绿（基线 `/tmp/evia-check/saved-export/SHA256SUMS`，Python 精确替换、禁 git checkout）
1. 删除 `assertSavedAnalysisActiveTx` 调用 → 归档用例如期变红，`sha256sum -c` 还原复绿。
2. 删除 `verifySavedMetricPinTx` 调用 → 原子/复合两个漂移用例如期变红，还原复绿。
3. 复合分支条件反转为 `false` → marker 用例如期变红（Bind marker shell 报 `INVALID_EVENT_SELECTION`），还原复绿。
每轮还原后哈希全部 OK，最终 `-count=1` 全绿。

### HTTP 边界（`transport/control`）
`TestSavedAnalysisExportCreateValidatesAndMaps` 断言版本路由参数映射、幂等头透传、64 位请求摘要、时间窗解析与 `ExportJob` 来源见证输出；`TestSavedAnalysisExportRejectsBadVersionAndBody` 覆盖 0/非数字版本与缺时间窗请求体在到达 service 前即 400。

### 前端与门禁
`tsc --noEmit` 通过；Playwright model 规格 114/114（`export-model.spec.ts` 新增 5 条：来源标签、版本导出构建、名称边界、时间窗预算、默认名称），临时 `playwright.model.config.ts` 用完即删；全量 `make check` EXIT=0、零 FAIL（Go 32 包、client 50、sdk 63、console 18、生产构建）。日志：`/tmp/evia-check/saved-export/make-check.log`。

## 明确未做（边界）

- 不新增导出格式（parquet/对象存储适配仍为既有缺口），不改变一次性下载授权与 15 分钟 TTL 语义。
- 版本导出不引入新的权限项，沿用 `data.export`；保存分析自身的归档/读取语义不变。
- 漏斗严格有序、身份关联图、来源系统字段分析化、热力图/会话回放、SMTP、多机房 TLS 与 TB 级容量仍待单独拍板或外部环境。
