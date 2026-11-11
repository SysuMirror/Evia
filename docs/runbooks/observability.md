# Process metrics and scraping

Every Evia process can expose operational metrics at `GET /metrics` in
Prometheus text exposition format 0.0.4. The endpoints are produced by the
standard-library-only `internal/platform/observe` package: no third-party
telemetry dependency is added to the self-hosted build.

## Exposure and security

The `serve` process always serves `/metrics` on its main management listener
(`EVIA_LISTEN`), alongside `/healthz` and `/readyz`. The four worker commands
(`ingest-worker`, `query-worker`, `erasure-worker`, `retention-worker`) have no
business HTTP server; they open a dedicated administrative listener only when
`EVIA_METRICS_LISTEN=host:port` is set. Leaving it empty disables the listener
entirely, which is the default and preserves deployments that do not yet scrape
workers. The worker admin listener exposes only `GET /metrics` and
`GET /healthz` (204); it carries no business routes.

All metrics endpoints are unauthenticated and must never be exposed on a public
network. Bind them to loopback or a private interface and restrict access with
the reverse proxy or network policy so only the monitoring network can reach
them; the management origin rules do not cover these endpoints because
scrapers do not carry browser sessions.

The response contains no event payloads, identities or project identifiers.
HTTP labels use ServeMux route templates (for example
`/api/v1/projects/{project_id}/retention-sweeps`), fixed methods and numeric
status codes, so series cardinality is bounded by the route table rather than
request volume. Scraping `/metrics` does not record itself, avoiding scrape
feedback in the request counters. `HEAD /metrics` is supported for lightweight
reachability checks; other methods return `405` with an `Allow: GET` header.
Each process reports its role on `evia_process_info`, so a single scrape target
per role keeps series attributable without adding a hostname label.

## Metric inventory

HTTP instruments are emitted by the `serve` process only (the worker admin
listener has no business mux), recorded by middleware around the full mux:

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `evia_http_requests_total` | counter | `method`, `route`, `code` | Completed requests; unmatched routes use `route="unmatched"`. |
| `evia_http_request_duration_seconds` | histogram | `method`, `route` | Handler latency with buckets from 5 ms to 25 s, plus `_sum`/`_count`. |
| `evia_http_requests_in_flight` | gauge | none | Requests currently being handled. |

Retention worker domain instruments, emitted by `retention-worker` from sweep
domain events. Labels use fixed enums only (`environment` is the policy
enum development/staging/production; `mode` is `dry-run` or `enforce`;
`result` is `succeeded` or `failed`), so cardinality stays bounded:

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `evia_retention_sweep_passes_total` | counter | `result` | Complete RunOnce evaluations over every policy; a failed policy listing is the only failed pass. |
| `evia_retention_sweep_pass_duration_seconds` | histogram | none | Wall-clock duration of one full sweep pass. |
| `evia_retention_sweeps_total` | counter | `environment`, `mode`, `result` | Per-scope sweep outcomes. |
| `evia_retention_sweep_candidates_total` | counter | `environment`, `mode` | Expired rows counted (dry-run) or deleted (enforcement); zero-candidate scopes emit no series. |
| `evia_retention_sweep_duration_seconds` | histogram | `environment`, `result` | Per-scope sweep duration. |

Erasure worker domain instruments, emitted by `erasure-worker` from job
lifecycle events. The `outcome` and claim `outcome` labels are fixed enums and
`code` is the durable fault vocabulary, so cardinality stays bounded:

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `evia_erasure_claim_polls_total` | counter | `outcome` | Claim attempts: `claimed`, `empty` (no queued job) or `error`. |
| `evia_erasure_jobs_total` | counter | `outcome`, `code` | Terminal job handling: `completed`, `failed` (with failure code), `lease-lost` or `failure-record-error`. |
| `evia_erasure_job_duration_seconds` | histogram | `outcome` | Wall-clock duration from claim to terminal handling. |

Ingest worker domain instruments, emitted by `ingest-worker` from Kafka batch
processing. Labels are fixed enums, so cardinality is independent of traffic:

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `evia_ingest_messages_polled_total` | counter | none | Kafka messages delivered in non-empty polls. |
| `evia_ingest_batches_total` | counter | `result` | Durable batch outcomes (`succeeded`/`failed`); shutdown cancellation is not counted. |
| `evia_ingest_records_total` | counter | `outcome` | Valid facts written (`ingested`) versus undecodable messages sent to quarantine (`quarantined`). |
| `evia_ingest_batch_duration_seconds` | histogram | `result` | Wall-clock duration of one durable batch. |

Query worker domain instruments, emitted by `query-worker` from query job
lifecycle events. One registry is shared by the whole worker pool, so counters
aggregate across every configured query worker. The `code` label is `ok` or
the durable `JobError` vocabulary and `retryable` is a boolean enum:

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `evia_query_claim_polls_total` | counter | `outcome` | Claim attempts: `claimed`, `empty` or `error`. |
| `evia_query_jobs_total` | counter | `code`, `retryable` | Durably completed queries; non-retryable codes such as `QUERY_COST_EXCEEDED` distinguish rejected requests from transient storage faults. |
| `evia_query_job_duration_seconds` | histogram | `code` | Wall-clock duration from claim to durable completion. |

Process and Go runtime samples are emitted by every process and computed at
each scrape:

| Metric | Type | Meaning |
| --- | --- | --- |
| `process_start_time_seconds` | gauge | Unix time when the process started. |
| `process_uptime_seconds` | gauge | Elapsed seconds since start. |
| `evia_process_info` | gauge (constant 1) | Identity metadata with `role` (`serve`, `ingest-worker`, `query-worker`, `erasure-worker` or `retention-worker`) and `goversion`. |
| `go_goroutines`, `go_threads` | gauge | Live goroutines and created OS threads. |
| `go_memstats_alloc_bytes`, `go_memstats_heap_inuse_bytes`, `go_memstats_sys_bytes`, `go_memstats_heap_objects`, `go_memstats_next_gc_bytes` | gauge | Heap and allocator state from `runtime.ReadMemStats`. |
| `go_memstats_alloc_bytes_total`, `go_gc_duration_seconds_total`, `go_gc_cycles_total` | counter | Cumulative allocation, stop-the-world GC time and GC cycles. |

## Example scrape configuration

```yaml
scrape_configs:
  - job_name: evia-serve
    metrics_path: /metrics
    scheme: https
    static_configs:
      - targets: ['evia.internal.example']
  # Workers are scraped on their dedicated EVIA_METRICS_LISTEN addresses.
  - job_name: evia-workers
    metrics_path: /metrics
    scheme: http
    static_configs:
      - targets:
          - 'evia-ingest.internal.example:19091'
          - 'evia-query.internal.example:19092'
          - 'evia-erasure.internal.example:19093'
          - 'evia-retention.internal.example:19094'
    # Keep both jobs on the restricted monitoring network; the endpoints have
    # no application-level authentication.
```

A minimal manual check against a local worker started with
`EVIA_METRICS_LISTEN=127.0.0.1:18091`:

```sh
curl -fsS http://127.0.0.1:18091/metrics | grep evia_process_info
```

## Alerting and SLO policy

Prometheus recording rules (short-window SLI ratios and p99 latency) and the
full critical/warning alert policy ship in
[`deploy/monitoring/prometheus-rules.yaml`](../../deploy/monitoring/prometheus-rules.yaml)
with promtool unit tests beside them. Every expression references only
instruments listed above; `tooling/check_monitoring.py` fails `make check` if a
rule drifts onto a metric no process emits. The SLO targets, alert inventory,
tuning policy and verification commands are documented in the
[SLO and alerting runbook](slo.md).

## Scope and follow-up work

All five process roles now expose the same process and Go runtime metrics, and
`serve` additionally reports HTTP traffic instruments. Every worker reports
domain instruments through an observer port defined in its own domain package,
keeping all domain packages free of the metrics implementation:
`SweepObserver` (retention-worker), `ErasureObserver` (erasure-worker),
`QueryObserver` (query-worker, shared registry across the worker pool) and
`IngestObserver` (ingest-worker). Worker domain metric coverage is therefore
complete; future extensions (for example Kafka consumer lag gauges) must follow
the same observer-port pattern and reuse the `observe` package so exposition
stays consistent, and any new alert must reference an emitted metric to pass
the monitoring gate. Grafana dashboards, Alertmanager routing and
backup/restore drill automation still do not ship in this repository. The
endpoints and rules report process health, traffic shape and failure burns;
they do not prove event-pipeline readiness, ClickHouse capacity or disaster
recovery, which still require the evidence listed in the deployment runbook.
