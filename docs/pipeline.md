# Durable event persistence

The collector publishes validated, credential-free internal records to Kafka. A Go consumer writes raw facts to ClickHouse and commits Kafka offsets only after both valid rows and any quarantine records have been acknowledged. This is an at-least-once persistence path. It does not implement Flink processing, identity linking, session inference, archive recovery or a production capacity guarantee.

## Queue acknowledgement and bounded work

`pipeline.KafkaProducer` implements the ingestion domain's `Queue` interface. Its Kafka client uses idempotent production and `acks=all`. `Publish` validates the complete local batch before sending and returns success only after every broker acknowledgement. Detected producer data loss stops publication. A missing or ambiguous acknowledgement returns an unavailable error; callers retry with the same event IDs. Producer idempotence protects a producer session, while the exact fact view also handles retries across process restarts.

Production topics need a reviewed broker policy: replication factor three, `min.insync.replicas=2` and unclean leader election disabled. The local single-broker fixture validates adapter behavior with replication factor one; it proves no host-failure or regional durability claim. Kafka acknowledgement guarantees depend on that broker policy.

Buffers are bounded by record count and bytes. The consumer permits one concurrent fetch, limits fetch bytes to eight MiB and rejects decompression batches beyond eight MiB. Broker message limits remain part of this memory budget. The consumer polls at most 128 records by default, stops polling while the sink or quarantine is unavailable, and retries with bounded exponential backoff. Each batch has a 30-second processing budget, below the configured 60-second rebalance timeout; an exhausted budget closes the consumer with unfinished offsets uncommitted, and a supervisor can restart it safely. It disables automatic offset commits and blocks partition rebalance while processing a batch. A new group starts at the earliest retained offset, while an out-of-range existing offset fails instead of silently resetting past missing history. A poll error stops the worker so a fresh consumer can resume from committed positions; it never advances past a partially observed batch.

Kafka partition keys hash authorized project, environment, source and declared identity. They do not expose a UID in plaintext or grant authority. Identity claims remain claims; they do not authorize platform data access.

## ClickHouse schema and exact reads

Apply `deploy/clickhouse/001_event_facts.sql` explicitly to the intended database with an operator-controlled migration procedure. The API and worker do not create, replace or delete tables on startup. The versioned script deliberately fails if applied twice; keep an applied-version/checksum ledger in deployment tooling. Do not apply it against an existing unrelated database or silently replace an existing table definition.

The initial raw table uses plain `MergeTree` with insert and part-directory fsync enabled. HTTP inserts are synchronous, disable asynchronous insert buffering, and wait for the query result. A replicated ClickHouse installation needs separately reviewed topology, quorum and migration settings; a successful single-node insert is not proof of disaster recovery.

The raw table stores typed project, environment, source kind, event identity, schema/release versions, timestamps, SDK metadata, identity and ordering/context columns. Missing identity/context values stay nullable. The exact context JSON preserves explicit empty values separately from omitted values. `event_time` currently equals `occurred_at`; no clock correction or inferred causality is performed. `received_at` retains nanosecond precision for deterministic receipt ordering.

Custom properties remain bounded JSON, preserving absent values, zero, false, arrays and numeric representation. The query adapter must resolve published types and bind property names as values, check `JSONHas` and `JSONType`, and then use the corresponding typed extraction. Property values never become SQL identifiers or fragments. Stable property IDs and typed property maps are a future storage migration; this initial JSON representation is not evidence of an accelerated layout.

All analytical reads use `event_facts`, which selects a complete winning row before event or property filters. The deduplication key is `(project_id, environment, source_id, event_id)`. The earliest `received_at` wins, with a canonical content hash breaking receipt-time ties. The content hash excludes receipt time, canonicalizes UUID case and object key order, and keeps business identifier case unchanged. Retries therefore cannot inflate counts, and a conflicting duplicate cannot create separate counts by changing a filter value. A repeated event ID from a different source is a separate fact, preventing an untrusted browser source from replacing a server source's fact.

Physical raw rows can contain retries and conflicting variants. Do not count the raw table, depend on background `ReplacingMergeTree` merges, or increment aggregates directly from replayed rows. Conflicting variants remain available for restricted diagnosis; this adapter does not implement a distributed conflict diagnostic stream or stateful identity resolver.

## Quarantine, shutdown and recovery

Malformed, unknown-version or invalid internal records are written to a separate private Kafka quarantine topic before the source offset is committed. Diagnostics contain a static reason, original topic/partition/offset and original bytes. They can contain sensitive event values and must not be exposed through public diagnostics or logs. Quarantine credentials, ACLs, retention and operator access need the same care as raw event data.

The quarantine key identifies the original Kafka position. A lost acknowledgement or failed offset commit can repeat a quarantine record; consumers of that topic must deduplicate by the original position. Configure its maximum message size for base64 framing expansion: a source maximum of one MiB needs a quarantine maximum of at least two MiB. If quarantine cannot durably accept a record, the worker stops advancing its offset and retries within the batch budget; subsequent supervised attempts resume from that offset. It never truncates or silently drops the original.

Cancel the worker context during graceful shutdown. Pending persistence or quarantine work remains uncommitted and is replayed by the next consumer. Drain the producer with a bounded `Close(ctx)` after outstanding collection requests have finished. A failed producer drain does not turn unacknowledged events into accepted events. A crash after a ClickHouse insert and before a Kafka commit replays the insert safely through the exact read view.

Monitor worker health, Kafka lag, retry stages, quarantine growth and ClickHouse insert failures. Repeated persistence failures should apply backpressure and require operator recovery, rather than acknowledge events that have not reached the persistence boundary. Restore schemas and sink availability before restarting a failed worker.

## Verification

Unit tests cover broker acknowledgement ordering, complete-batch validation, bounded retries, persistence/quarantine-before-commit ordering, cancellation without offset advancement, strict internal decoding, context presence, safe fixed SQL and failure responses.

Explicit integration tests require `EVIA_TEST_KAFKA_BROKERS` and `EVIA_TEST_CLICKHOUSE_URL`, with optional `EVIA_TEST_CLICKHOUSE_USERNAME` and `EVIA_TEST_CLICKHOUSE_PASSWORD`. They fail if required configuration is absent and create/drop only uniquely named synthetic topics and databases. Run `go test -race -tags=integration ./internal/pipeline -count=1` with these variables. Tests exercise real Kafka acknowledgements, actual ClickHouse persistence with an intentionally lost acknowledgement, conflict filtering after exact deduplication, quarantine records, committed-offset resume and full consumer-group replay. These tests do not establish throughput or disaster-recovery capacity.
