# HTTP ingestion benchmark

Build this command from the backend module. It sends only synthetic events to
`POST /v1/collect` on a literal loopback HTTP address. Events must already have a
published, active definition and a usable browser or server source. Acceptance
means every returned receipt item matches its request; query visibility requires
the separate real-dependency benchmark harness.

`--config` or `EVIA_BENCH_CONFIG` identifies a regular, mode-0600 JSON file, at
most 16 KiB. Required fields are `contract_version` (1), `api_url`, `project_id`,
`environment` (`development`), `run_id` (fresh UUID per dataset), `source_kind`,
`write_key`, `event_key`, `schema_version`, `release_revision`, and `started_at`
(UTC RFC3339). Server sources require `server_secret`; browser sources require
`origin`. Optional fields are `source_id` and `uid_cardinality` (default 100000).
Unknown fields and ambiguous JSON fail. Credential values never enter reports.

Run with explicit workload and output arguments:

```sh
EVIA_BENCH_CONFIG=/tmp/private-benchmark.json ./evia-bench \
  --events 100000 --batch-size 50 --concurrency 8 \
  --replay-percent 10 --out /tmp/benchmark-ingestion.json
```

No events are sent without `--events` and `--out`. Batch size is at most 50,
concurrency at most 128, and each HTTP attempt has a 10-second timeout. The
command stops after 24 minutes or an interrupt and writes incomplete evidence.
There are no automatic retries, including for 429 or 5xx responses. An explicit
replay phase sends the first `floor(events * replay-percent / 100)` events using
the exact original batch IDs and bytes. Batch boundaries split at that prefix.
Replaying requires the same run, event count, configuration and batch size.

With `--rate 0` (default), workers use closed loop concurrency. This measures
request service latency and does not correct for coordinated omission. Positive
`--rate` schedules offered events per second with no pending queue: whole batches
drop when all workers are busy. Reports include drops and maximum scheduler lag.
Latency percentiles use bounded logarithmic buckets with 1.02 upper bounds; the
maximum is exact. Request bytes count JSON bodies, excluding headers and TCP.
Throughput uses total run time, including the replay phase.

For ordinal `i`, the UID is `bench-{run_id}-{i % uid_cardinality}`. Properties are
`page=/page/{i%1000}`, `section=section-{i%10}`, `post_id=post-{i%10000}`,
`duration_ms=i%1000`, `value=i%100`, and `tag=tag-{i%20}`. UUIDs derive from
SHA-256 of `{run_id}/{source_kind}/{role}/{ordinal}`, with UUID v4 and variant
bits. Session and tab ordinals use `i % uid_cardinality`. The fixed event window
is 30 minutes before `started_at`; ordinal timestamps add
`floor(i * 1800e9 / events)` nanoseconds. Existing admission rejects events older
than one hour, so prepare a fresh configuration immediately before each run.

The atomic mode-0600 JSON report separates `unique` and `replay` attempted,
accepted, permanently rejected, retryably rejected, 429, 5xx, unexpected and
dropped event counts. It also records request counts, safe bounded failure
ordinals, latency, elapsed time, fixed event window and expected exact count,
UID cardinality, value sum, mean duration and section-zero count. `complete`
requires every planned unique and replay event to receive an accepted receipt.
An incomplete result exits 1; configuration errors exit 2. Reports contain only
synthetic scope identifiers and numeric evidence, excluding request bodies,
response messages, API addresses and credentials.
