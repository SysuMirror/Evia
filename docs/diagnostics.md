# Authorized collection diagnostics

`GET /api/v1/projects/{project_id}/diagnostics` returns one authorized, read-only diagnostic report for a project. It is mounted by `control.DiagnosticsAPI` behind `projectAuth`, so any project member or a project-scoped machine credential with route access can read it, but no event payloads, secrets or credentials are ever returned. The report aggregates four independent sections; each section degrades on its own dependency and never invents measurements.

## Report sections

### Feature switches

`features` reports the serve-process switches at request time: `collection_enabled`, `analysis_enabled` and `delivery_enabled`. They explain which downstream sections can exist at all.

### Source inventory

`sources` reads the durable PostgreSQL source listing through the same `ListSources` port as the sources workspace, bounded to 200 rows. It reports the total count, status and environment distributions, and up to 20 disabled sources. When the source secret key is not configured, the section renders `disabled`; a PostgreSQL failure fails the whole request because the inventory is the report's primary configuration signal.

### Collection budget

`budget` is present only when collection is enabled. It combines a live Redis `Ping` with the exact configured policy snapshot from `budget.Limiter.SnapshotPolicies`: the default rate/burst and one effective policy per supported environment, marking explicit overrides. A failed ping renders status `unavailable` with code `DIAGNOSTICS_BUDGET_UNAVAILABLE` while still returning the configured policy. With collection disabled the section renders `disabled` and performs no Redis I/O.

### Pipeline readiness

`pipeline` reads the ClickHouse `pipeline_readiness` table through `pipeline.ReadinessReader`, grouping the newest probe receipt per environment/release/revision (at most 50 rows). Status is `ok` when receipts exist, `no_data` when the retention window has no receipt for the project, `unavailable` when ClickHouse cannot be queried, and `disabled` when neither analysis nor delivery is enabled. Each proof carries its last observation time, age in seconds and durable proof count. The reader queries only the receipt table and never the event fact tables.

### Rejection accounting

`rejections` is present only when collection is enabled. Durable counters live in Redis through `internal/platform/admissionstats`:

- One hash per project/environment/hour, hash-tagged for cluster-slot compatibility: `evia:admission:{project:environment}:<unix-hour>`.
- Fields are a bounded rejection-code vocabulary (for example `INVALID_EVENT`, `SCHEMA_NOT_READY`, `COLLECTION_BUDGET_EXCEEDED`); unknown codes are dropped so hash cardinality stays controlled.
- Every key expires after 25 hours, so reads need no `SCAN` and memory cannot grow without bound. Reads deterministically address the last 24 hourly buckets per known environment.
- The collector aggregates item-level and budget rejections within a batch and flushes them once through the fail-open `ingestion.RejectionSink`. A Redis failure increments an internal dropped-write counter but never changes the admission response.

## Attribution boundary

Only rejections with a resolved authenticated source are attributable to a project and environment, so only those are counted. Malformed batches (`BAD_BATCH`, `BAD_BATCH_TIME`) and source authentication failures happen before a project scope exists and are intentionally not attributed; they remain visible through HTTP responses and the process Prometheus instrumentation. Rejection telemetry uses a detached, bounded context so request cancellation cannot skip the final accounting of an already-decided rejection.

## Status vocabulary

Every section uses the same bounded vocabulary: `ok`, `no_data`, `unavailable`, `disabled`. The console maps these to fixed Chinese labels and tones, and the OpenAPI schema enforces the enums and array bounds (20 disabled sources, 50 proofs, 48-hour maximum window).

## Evidence and tests

- Budget snapshot and Redis stats: unit tests plus real Redis integration tests in `internal/platform/admissionstats` and `internal/platform/budget`.
- Collector sink: fail-open aggregation tests in `internal/ingestion/collector_rejection_test.go`.
- Readiness reader: real ClickHouse integration test covering grouping, scope isolation and empty results.
- Domain degradation and HTTP mapping: `internal/diagnostics` and `internal/transport/control/diagnostics_test.go`.
- Full-stack evidence: a real `POST /v1/collect` batch with invalid events produces `INVALID_EVENT` counters visible both through the endpoint and directly in Redis, with the expected ~25-hour TTL.

## Known limits

- Counters cover the last 24 hours only; long-term rejection history is not retained.
- Redis loss resets counters; the section reports unavailability rather than reconstructed estimates.
- Pre-source and unauthenticated failures are not project-attributable by design.
- Per-replica process metrics remain available separately through the `/metrics` Prometheus endpoint; this API reports shared durable state, not process-local counters.
