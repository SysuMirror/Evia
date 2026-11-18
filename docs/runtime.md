# Runtime processes and explicit feature configuration

The executable separates API serving, event materialization and durable query execution. Feature flags are off by default. The existing control-only API can run without Redis, Kafka or ClickHouse, and startup never applies database or ClickHouse schema changes implicitly.

## Commands

| Command               | Responsibility                                                                                                                                                                                                                                                  | Required feature flag               |
| --------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------- |
| evia serve            | Management HTTP API, optionally source CRUD, collection and analysis routes                                                                                                                                                                                     | Flags below, default false          |
| evia ingest-worker    | Acknowledged Kafka records to ClickHouse facts, then explicit Kafka offset commits                                                                                                                                                                              | EVIA_COLLECTION_ENABLED=true        |
| evia query-worker     | A bounded pool claiming durable PostgreSQL analysis jobs and executing ClickHouse queries                                                                                                                                                                       | EVIA_ANALYSIS_ENABLED=true          |
| evia erasure-worker   | Claims durable erasure jobs, writes the resurrection tombstone before issuing synchronous ClickHouse ALTER DELETE mutations and verifies zero rows remain                                                                                                       | None (uses ClickHouse + PostgreSQL) |
| evia retention-worker | Evaluates every configured retention policy on an interval and records an audit row per scope. Defaults to dry-run (counts expired facts only); with `EVIA_RETENTION_ENFORCEMENT_ENABLED=true` it issues bounded, time-scoped ClickHouse ALTER DELETE mutations | None (uses ClickHouse + PostgreSQL) |
| evia migrate          | Explicitly apply embedded PostgreSQL migrations                                                                                                                                                                                                                 | None                                |
| evia bootstrap        | Create the initial synthetic or operator-provided administrator once                                                                                                                                                                                            | None                                |
| evia export-worker    | Claims durable export jobs and materializes bounded CSV/JSON/NDJSON artifacts under the export directory, guarded by row/byte caps and sha256 fingerprints                                                                                                                | EVIA_EXPORT_DIR (ClickHouse + PostgreSQL) |
| evia backfill-worker  | Re-ingests archived JSONL facts through a dedicated inserter, comparing row counts against the archive manifest and failing loudly on drift                                                                                                                     | EVIA_ARCHIVE_DIR (ClickHouse + PostgreSQL) |

Every long-running process needs EVIA_DATABASE_URL. The one-shot account administration commands `account-create`, `account-disable`, `account-enable` and `account-reset-password` also read only EVIA_DATABASE_URL plus their private account environment values; they are operator CLI actions rather than services and are documented in the [account runbook](runbooks/accounts.md). Workers deliberately do not require the browser origin, cookie key or source encryption key. Apply the matching PostgreSQL migrations before serving or starting workers. Apply the reviewed ClickHouse schema in deploy/clickhouse before materialization/query use, and provision Kafka topics explicitly. The executable does not silently create topics, execute DDL or activate releases. Release readiness is established only by the explicit verified activation flow described below.

For API serving, EVIA_PUBLIC_ORIGIN is a single ASCII http(s) origin with no path, query, fragment or credentials; host case, default ports and IPv6 literals are normalized to browser form. EVIA_CURSOR_KEY is 32 random bytes encoded as 64 hexadecimal characters. EVIA_LISTEN defaults to 127.0.0.1:8080. Secure cookies are the default; plain HTTP requires EVIA_ALLOW_INSECURE_HTTP=true. Neither a source write credential nor a Kafka acknowledgement grants management access.

## Optional surfaces

- EVIA_SOURCE_SECRET_KEY is a stable 32-byte encryption key encoded as 64 hexadecimal characters. Providing it enables source-management endpoints independently of collection; omitting it leaves those routes disabled. Keep it private and backed up. Do not generate a different key on each restart. Existing collector credentials can be resolved without enabling source CRUD.
- EVIA_COLLECTION_ENABLED=true registers /v1/collect with the real cached source resolver, Redis admission budget and acknowledged Kafka producer. It requires Redis and Kafka settings. Invalid or absent feature flags never imply an unlimited collector or a mock queue.
- EVIA_ANALYSIS_ENABLED=true registers management query routes backed by PostgreSQL jobs and the real ClickHouse engine. The API only enqueues work; run query-worker separately to execute it.
- EVIA_RETENTION_ENFORCEMENT_ENABLED=true switches retention-worker from the default dry-run (count only, no deletion) to enforcement that actually deletes expired facts. Leave it unset until the dry-run audit rows in `retention_sweeps` confirm the candidate counts. EVIA_RETENTION_SWEEP_INTERVAL bounds the evaluation cadence to between `1m` and `24h` (default `6h`).

The source cache is bounded and has a two-second successful-entry lifetime. Local source mutations clear it immediately. The collector also bounds cached manifests and source/cache misses. These bounds do not establish production request capacity.

## Dependency configuration

| Variable                                            | Meaning or default                                                            |
| --------------------------------------------------- | ----------------------------------------------------------------------------- |
| EVIA_REDIS_URL                                      | Redis endpoint, required for API collection                                   |
| EVIA_REDIS_KEY_PREFIX                               | evia:collection, isolate deployments intentionally                            |
| EVIA_COLLECTION_RATE                                | 1000 attempted events/second per project/environment                          |
| EVIA_COLLECTION_BURST                               | 2000 events, must cover the 50-event batch maximum                            |
| EVIA_REDIS_POOL_SIZE                                | 32, bounded to 1–256 active connections per API process                       |
| EVIA_REDIS_TIMEOUT                                  | 200ms, positive and at most 5s                                                |
| EVIA_KAFKA_BROKERS                                  | Comma-separated host:port endpoints, at most 16                               |
| EVIA_KAFKA_TOPIC                                    | Explicit event topic                                                          |
| EVIA_KAFKA_DEAD_LETTER_TOPIC                        | A distinct explicit quarantine topic                                          |
| EVIA_KAFKA_GROUP                                    | Stable materialization consumer group, required for ingest-worker             |
| EVIA_KAFKA_TLS_ENABLED                              | true                                                                          |
| EVIA_KAFKA_USERNAME / EVIA_KAFKA_PASSWORD           | Optional SASL credentials, supplied together and only with TLS                |
| EVIA_KAFKA_PROBE_TOPIC                              | Reserved separate readiness-control topic, no business fact traffic           |
| EVIA_CLICKHOUSE_URL                                 | HTTP interface endpoint with no credentials in the URL                        |
| EVIA_CLICKHOUSE_DATABASE                            | Explicit migrated database identifier                                         |
| EVIA_CLICKHOUSE_USERNAME / EVIA_CLICKHOUSE_PASSWORD | Credentials supplied together, use appropriately restricted users per process |
| EVIA_QUERY_WORKERS                                  | 2 for query-worker, bounded to 1–16 per process                               |
| EVIA_INSTANCE_ID                                    | evia, a bounded process-label prefix                                          |

Every non-local Redis endpoint must use rediss, Kafka uses TLS by default, and ClickHouse requires HTTPS. EVIA_ALLOW_INSECURE_DEPENDENCIES=true permits plaintext only for localhost or loopback IP endpoints. Turning Kafka TLS off still requires that flag and every broker to be loopback. Kafka password authentication always requires TLS. Certificate verification remains enabled; use the system certificate store for private certificate authorities. URL query parameters, fragments and embedded ClickHouse credentials are rejected.

Each query worker receives a distinct identifier containing a random process UUID and worker index. Job ownership is a durable PostgreSQL lease with a generation fence; an in-memory goroutine is not proof of ownership. The existing worker limits query execution to 30 seconds, monitors cancellation/access changes, bounds results, and completes only while the claimed lease is valid. Multiple worker processes can share the same queue; per-project claim limits remain enforced transactionally by the store.

## Verified release delivery

With collection enabled, supplying EVIA_KAFKA_PROBE_TOPIC enables the activation and source-scoped public configuration routes. This also requires ClickHouse settings even when analysis HTTP is disabled. Omitting the probe topic preserves collection-only operation and leaves delivery routes disabled; a topic name alone never creates a readiness proof.

The API produces a scoped, expiring nonce to the separate readiness topic and waits for a matching persisted receipt. The ingest-worker command runs three supervised tasks when delivery is configured: fact materialization, readiness receipt processing, and readiness lease renewal. The receipt consumer has a separate consumer group derived by appending `-readiness` to EVIA_KAFKA_GROUP. It verifies actual fact table/view types and required grants before writing control receipts. Readiness records go to a dedicated control table, never the business fact table.

Provision the readiness topic and apply the reviewed ClickHouse readiness and precision migrations before enabling delivery. PostgreSQL activation/readiness schema migrations must also be applied explicitly. The default delivery service uses a 10-second probe deadline, a one-minute readiness lease and a 24-hour compatibility window. A failed refresh does not extend the lease. A stopped or failing worker therefore cannot leave readiness permanently true; the collector and public configuration recheck actual scope, activation, manifest hash and lease expiry.

Activation is an authenticated management mutation with revision and idempotency checks. Public configuration uses the source credential and its allowed origin, exposes only the source's permitted event definitions, and does not accept a platform session cookie as authorization. Metadata builds, activation, current pipeline readiness and queryability remain separate outcomes.

## Startup, health and shutdown

Processes validate configuration before opening the HTTP listener or starting workers. Startup checks schema migration integrity. Enabled serving dependencies are probed using the existing Redis pool, Kafka producer and an authenticated bounded SELECT 1 against the configured ClickHouse database. Health checks consume no collection allowance and write no business fact rows.

/healthz reports that the API process is serving. /readyz checks PostgreSQL and only the enabled collection/analysis dependencies with a bounded context; a failed check returns 503. Dependency connectivity is not a release activation or an ingestion-readiness proof. A collector still checks actual scoped release readiness and fails closed when plans are unavailable or inactive.

HTTP operations have bounded contexts and server timeouts. SIGINT/SIGTERM cancels workers and stops accepting HTTP requests. The API has a ten-second graceful HTTP drain, worker supervision has a fifteen-second join limit, and each independent acknowledged Kafka producer gets a separate five-second final drain. Unfinished ingestion offsets remain uncommitted for replay. Query leases and generation fences prevent stale workers from committing late outcomes.

Any unexpected worker return, including a clean early return, stops its sibling tasks and reaches the process exit status. A worker failure is not silently discarded in a background goroutine. Fatal external errors are wrapped with safe stage names; process logs never print raw driver errors, connection URLs, passwords, source credentials or event bodies. Cleanup failures also stop the supervised API instead of being silently ignored.

## Verification boundary

Configuration tests exercise feature opt-in, the control-only baseline, independent worker configuration, TLS/plaintext restrictions, finite rates, bounded concurrency, identifiers and safe error messages. Runtime tests verify failure propagation, sibling cancellation, shutdown deadlines, listener bind failure, exact authenticated health responses and secret-safe stage wrapping. Redis correctness evidence is recorded in docs/budget.md.

A real combined executable smoke must separately demonstrate the configured API, materializer and query worker against PostgreSQL, Redis, Kafka and ClickHouse with synthetic data. Passing configuration or lifecycle unit tests alone does not establish that pipeline. Production capacity, restart/failover recovery and full release delivery readiness require their own evidence.
