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

## Scope and follow-up work

All five process roles now expose the same process and Go runtime metrics, and
`serve` additionally reports HTTP traffic instruments. Domain-level worker
instruments — Kafka consumer lag and batch outcomes for `ingest-worker`,
query job lease and execution counts for `query-worker`, erasure state
transitions for `erasure-worker`, and sweep/mutation counters for
`retention-worker` — are not yet recorded; they must reuse the same `observe`
package so exposition stays consistent. No alerting rules, dashboards, SLO
definitions or backup/restore drill automation ship in this repository yet.
The endpoints report process health and traffic shape; they do not prove
event-pipeline readiness, ClickHouse capacity or disaster recovery, which still
require the evidence listed in the deployment runbook.
