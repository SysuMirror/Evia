# Process metrics and scraping

The `evia serve` process exposes operational metrics at `GET /metrics` in
Prometheus text exposition format 0.0.4. The endpoint is produced by the
standard-library-only `internal/platform/observe` package: no third-party
telemetry dependency is added to the self-hosted build.

## Exposure and security

`/metrics` is registered on the same listener as the management API and, like
`/healthz` and `/readyz`, is unauthenticated. It must never be exposed on a
public network. Restrict the listener with the reverse proxy or network policy
so only the monitoring network can reach it; the management origin rules do not
cover this endpoint because scrapers do not carry browser sessions.

The response contains no event payloads, identities or project identifiers.
HTTP labels use ServeMux route templates (for example
`/api/v1/projects/{project_id}/retention-sweeps`), fixed methods and numeric
status codes, so series cardinality is bounded by the route table rather than
request volume. Scraping `/metrics` does not record itself, avoiding scrape
feedback in the request counters. `HEAD /metrics` is supported for lightweight
reachability checks; other methods return `405` with an `Allow: GET` header.

## Metric inventory

HTTP instruments, recorded by middleware around the full mux:

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `evia_http_requests_total` | counter | `method`, `route`, `code` | Completed requests; unmatched routes use `route="unmatched"`. |
| `evia_http_request_duration_seconds` | histogram | `method`, `route` | Handler latency with buckets from 5 ms to 25 s, plus `_sum`/`_count`. |
| `evia_http_requests_in_flight` | gauge | none | Requests currently being handled. |

Process and Go runtime samples, computed at every scrape:

| Metric | Type | Meaning |
| --- | --- | --- |
| `process_start_time_seconds` | gauge | Unix time when the process started. |
| `process_uptime_seconds` | gauge | Elapsed seconds since start. |
| `evia_build_info` | gauge (constant 1) | Build metadata, currently the `goversion` label. |
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
    # Keep this job on the restricted monitoring network; the endpoint has no
    # application-level authentication.
```

A minimal manual check:

```sh
curl -fsS https://evia.internal.example/metrics | head
```

## Scope and follow-up work

This slice instruments the `serve` process only. The `ingest-worker`,
`query-worker`, `erasure-worker` and `retention-worker` processes currently
expose no metrics listener; their queue depth, lease, sweep and mutation
instruments are a separate delivery and must reuse the same `observe` package
so exposition stays consistent. No alerting rules, dashboards, SLO definitions
or backup/restore drill automation ship in this repository yet. The endpoint
reports process health and traffic shape; it does not prove event-pipeline
readiness, ClickHouse capacity or disaster recovery, which still require the
evidence listed in the deployment runbook.
