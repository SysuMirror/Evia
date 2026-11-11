# Service level objectives and alerting

This runbook defines the Evia SLO policy and the Prometheus rules that enforce
it. Rules live in [`deploy/monitoring/prometheus-rules.yaml`](../../deploy/monitoring/prometheus-rules.yaml)
and only reference instruments the binaries actually expose; the metric
inventory is in the [observability runbook](observability.md).

## Loading the rules

Add the rule file to the Prometheus configuration that scrapes Evia and reload
Prometheus:

```yaml
rule_files:
  - /etc/prometheus/rules/evia/prometheus-rules.yaml
```

Scrape jobs must be named with the `evia` prefix (for example `evia-serve`,
`evia-workers`) because several selectors use `job=~"evia.*"` to avoid matching
unrelated targets. Rule groups evaluate every 30 seconds. Alert routing into
paging or chat systems is an Alertmanager concern and is not part of this
repository.

## SLO definitions

The objectives below are the **initial policy** for a production deployment.
They are deliberate targets, not measured baselines: tune them against the
first real traffic cycles and record every change here rather than editing
thresholds ad hoc in the rule file.

| Service | SLI (recording rule) | Objective | Window | Rationale |
| --- | --- | --- | --- | --- |
| Control API | Non-5xx request ratio (`evia:slo_http_availability:ratio5m`) | 99.9% | 30 days | Client-disconnect requests are not counted as 5xx after the context-cancellation boundary fix; the budget is about 43 minutes/month. |
| Control API latency | Worst-route p99 (`evia:slo_http_latency_p99:seconds5m`) | p99 below 2 s | 30 days | Sustained breach warns before availability is consumed; histogram buckets start at 5 ms. |
| Ingest pipeline | Durable batch success ratio (`evia:slo_ingest_success:ratio5m`) | 99.95% | 30 days | A failed batch is retried, so sustained failure means the event path is blocked, not merely degraded. |
| Query pipeline | Success ratio excluding client rejections (`evia:slo_query_success:ratio5m`) | 99.9% | 30 days | Non-retryable rejections such as `QUERY_COST_EXCEEDED` are client behavior and watched separately, never charged to this SLO. |
| Data erasure | Completed erasure jobs (`evia_erasure_jobs_total`) | 100% completion, zero failed jobs | Per job | Compliance process: any failed job is an incident with the tombstone still protecting the subject. |
| Retention enforcement | Enforced sweeps (`evia_retention_sweeps_total`) | Zero failed enforced sweeps | Per evaluation cycle | Expired data surviving past policy is a compliance gap; dry-run failures warn through the same sweep metrics. |

Ratio rules produce no sample (NaN) while their denominator is zero, so idle
processes cannot burn error budget. Burn alerts additionally require a traffic
floor so a single failure on a near-idle job does not page.

## Alert inventory

### Critical

| Alert | Trigger | First response |
| --- | --- | --- |
| `EviaScrapeTargetDown` | An `evia.*` scrape target is unreachable for 5 minutes | Check process supervision and the monitoring network; workers without `EVIA_METRICS_LISTEN` have no listener by design. |
| `EviaHTTPServerErrorBurn` | Over 5% 5xx for 10 minutes above 0.2 req/s | Correlate serve logs by `request_id`; check `/readyz` and PostgreSQL/Redis reachability. |
| `EviaIngestBatchFailureBurn` | Durable batch failures in every 5-minute window for 5 minutes | Check Kafka and ClickHouse; inspect quarantine/dead-letter diagnostics. |
| `EviaQueryRetryableFailureBurn` | Retryable query failures for 10 minutes | Treat as storage trouble; check ClickHouse health and the query memory budget (fault code 241). |
| `EviaErasureJobFailed` | Any erasure job reaches the failed terminal state | Read the durable failure code; the tombstone stays active and writes remain blocked until resolved. |
| `EviaRetentionSweepFailed` | A per-scope sweep failed within 6 hours | Inspect `retention_sweeps` audit rows and ClickHouse mutation status. |
| `EviaRetentionSweepPassFailed` | A full policy-listing pass failed within 6 hours | Check PostgreSQL connectivity and the worker lease; no scope was enforced during the pass. |
| `EviaErasureClaimPollErrors` | Erasure queue claim polls error for 10 minutes | Queued erasure requests are stalling; check PostgreSQL connectivity and permissions. |
| `EviaQueryClaimPollErrors` | Query queue claim polls error for 10 minutes | Submitted analyses are not being picked up; check PostgreSQL connectivity and permissions. |

### Warning

| Alert | Trigger | First response |
| --- | --- | --- |
| `EviaHTTPLatencyHigh` | Worst-route p99 above 2 s for 15 minutes | Compare per-route latency and in-flight requests before capacity changes. |
| `EviaQuarantineRatioHigh` | Over 5% quarantined records for 30 minutes above 0.1 records/s | Inspect producer schemas and dead-letter diagnostic reasons. |
| `EviaConsumerLagHigh` | Any partition lag above 1000 messages for 10 minutes | Check ingest throughput, ClickHouse write health and backlog replay; the lag/committed/log-end gauges identify the partition. |
| `EviaQueryClientRejectionBurn` | Over 10% non-retryable client rejections for 30 minutes | Adjust client workloads or review query capacity assumptions; not an SLO breach. |
| `EviaProcessRestarted` | `process_start_time_seconds` changed within 15 minutes | Check supervisor logs and decide whether the restart was planned. |
| `EviaGoroutineLeak` | Over 500 goroutines for 15 minutes | Correlate with `go_threads` and heap growth; look for blocked dependency calls. |

Alerts intentionally avoid traffic-shape assumptions (for example "no ingest
for N minutes"): legitimate low-traffic deployments must not produce false
pages. Add such alerts as site-specific rules rather than changing the shared
policy.

## Grafana dashboard

[`deploy/monitoring/grafana-dashboard.json`](../../deploy/monitoring/grafana-dashboard.json)
is a Grafana 11 dashboard covering six rows: control API (availability,
request rate, p99 latency, in-flight), ingest (success ratio, record outcomes,
batch results, batch p95, per-partition consumer lag), query (success ratio,
codes, job p95), erasure (jobs per hour, claim polls), retention (sweeps and
candidates) and process health (scrape state, goroutines, OS memory, uptime).
Import it through
Dashboards → New → Import and select the Prometheus datasource when prompted
for the `DS_PROMETHEUS` variable; the dashboard contains no hardcoded datasource
UID. The monitoring gate walks every dashboard expression with the same
emitted-metric allowlist as the rules, so a panel referencing a renamed or
nonexistent metric fails `make check`.

## Verification

Two independent gates protect the rules:

1. Repository-native static gate, wired into `make check` via
   `tooling/check_monitoring.py`: every metric referenced by an expression — in
   the rule file, the promtool test or the Grafana dashboard — must be
   registered in non-test Go sources (or be the Prometheus built-in `up` or a
   recording rule defined in the rule file), alerts must carry a severity and
   summary/description annotations, and the rule test may only reference real
   metrics and alerts. The gate ships with Python self-tests in
   `tooling/test_check_monitoring.py`.
2. PromQL semantics and alert behavior, checked with promtool:

```sh
promtool check rules deploy/monitoring/prometheus-rules.yaml
promtool test rules deploy/monitoring/prometheus-rules.test.yaml
```

The committed test file feeds synthetic series to every alert family and
asserts both firing behavior (including rendered annotations) and healthy
silence, plus exact SLI recording-rule values. promtool is not a runtime
dependency of Evia; install it locally with the pinned Go toolchain, for
example `go install github.com/prometheus/prometheus/cmd/promtool@v0.316.0`
when the module proxy mirrors that version.

## Scope and non-goals

The rules and dashboard detect process, pipeline and compliance failures. They
do not prove ClickHouse capacity at the target scale (see the query capacity
report), do not replace backup/restore drills, and no Alertmanager
routing/notification tree ships in this repository. SLO compliance reporting
over 30-day windows requires the Prometheus retention policy to cover at least
that window; recording rules here are short-window SLIs, not long-term
compliance archives.
