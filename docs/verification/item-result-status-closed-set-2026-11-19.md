# 采集回执条目状态闭集收敛（CollectResponse item status）验证记录（2026-11-19）

## 问题（确定性缺口）

采集回执 `ItemResult.Status` 只有两个词汇 `accepted`/`rejected`，但 [collector.go](../../backend/internal/ingestion/collector.go) 以 4 处裸字符串赋值，跨包测试另有 5 处裸比较。契约侧该词汇不是 enum 数组，而是 `CollectResponse.results.items.oneOf` 双分支 sum type（accepted 分支带 event_id、rejected 分支带 error，各自 `status` 为 JSON Schema `const`）。Go 侧没有任何机械守卫与这两个 const 绑定，单边改动不会在构建期暴露。

## 方案（行为不变的纯机械收敛）

1. 新增 [item_result_policy.go](../../backend/internal/ingestion/item_result_policy.go)：`ItemStatusAccepted`/`ItemStatusRejected` 常量与 `ItemStatuses` 切片，作为 Go 侧单一真相源（与同包 `source_policy.go` 同构）；
2. 新增 [item_result_contract_test.go](../../backend/internal/ingestion/item_result_contract_test.go)：`TestItemResultStatusVocabularyMatchesContract` 遍历 `oneOf` 两个分支提取 `status.const`，先断言分支数等于闭集大小、再排序做集合相等比较，零外部依赖；
3. collector.go 4 处生产赋值迁移到常量；5 个测试文件的 Go 结构体比较迁移（ingestion 包内非限定、transport/pipeline 外部包 `ingestion.` 限定）；
4. 刻意保留：transport 层 3 处 `strings.Contains(body, "accepted")` 是对线上 HTTP 响应字节的 wire 级见证，不属于 Go 词汇表，保持字符串字面量。

## 守卫有效性取证（变异红绿）

基线 sha256（item_result_policy.go / item_result_contract_test.go）存 `/tmp/evia-check/item-status/sha256-baseline.txt`，原件备份于 `baseline/`。

- 变异 A（闭集单边扩项）：向 `ItemStatuses` 注入 `"drift_probe"` → 守卫在分支数断言报 `item result status drift: contract branches 2 != Go [accepted rejected drift_probe]` 变红；
- 变异 B（常量值漂移）：把 `ItemStatusRejected` 的值改为 `"drift_probe"`（分支数仍为 2）→ 守卫在集合断言报 `item result status drift: contract [accepted rejected] != Go [accepted drift_probe]` 变红；
- 两条断言分支均被独立证有效。两次变异从备份还原，`sha256sum -c` 全部 OK，复跑复绿，`grep drift_probe internal/ingestion/` 零残留。

## 验证

- gofmt 零差异；`go build ./...` 通过；`go test ./internal/ingestion/` 与 `./internal/transport/control/` 全绿；
- 全量 `make check` EXIT=0、零 FAIL：Go 33 包 ok（`go test -race ./...`）、api-client 51、browser-sdk 63，命名/契约/边界/prettier/typecheck/生产构建全过。日志：`/tmp/evia-check/item-status/make-check.log`；
- 真实依赖回归（Kafka + ClickHouse）：`go test -race -tags integration ./internal/pipeline` 中本切片触及的两个核心测试 `TestSystemEventsFlowCollectorKafkaClickHouse`（0.63s，即字面量迁移所在测试）与 `TestKafkaClickHouseDurableReplayAndQuarantine`（3.48s）显式复跑 PASS。日志：`/tmp/evia-check/item-status/pipeline-integration.log`。
- 同包两个失败与本切片无关，属 2026-11-15 已记录的环境门控（见 [system-public-config-e2e](system-public-config-e2e-2026-11-15.md)）：`TestReplicatedSinkBaseline` 需 `EVIA_TEST_CH_HA_LB_URL/NODE1_URL/NODE2_URL` 单节点沙箱缺失即 t.Fatal；`TestReadinessReaderGroupsAndScopesProofs` 在干净 HEAD（还原本切片唯一 pipeline 改动后）以同一错误 `expected two grouped proofs for project A, got []` 复现，单节点沙箱不具备其前置数据面。

## 明确未做（边界）

- 不改动契约 oneOf 结构、wire 生成代码与任何运行时行为；
- 不引入 `ValidItemStatus`：状态只由 collector 内部赋值、无从边界入站校验的调用点，避免无人调用的助手；
- HTTP body 子串见证保持裸字符串，作为序列化输出的独立证据。
