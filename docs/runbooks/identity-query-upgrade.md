# Exact identity query access paths

Migration `005_identity_projections.sql` adds ordinary UID and anonymous-ID
projections to `event_facts_raw`. It first enables nullable projection sort keys
through `allow_nullable_key=1`. The base table's partition and sort keys do not
change. Apply migrations explicitly, in numeric order, to the intended database
before deploying the query worker. Application startup does not apply them.
Migration 004 is required by compact trend readers. Migration 005 is optional
for query correctness, but its materialized access paths keep selective identity
queries within the same scan budgets on large datasets.

The planner selects one necessary raw identity equality: an explicit
`identity_filter`, a direct system `uid`/`anonymous_id` equality, or an equality
under only `and` nodes. It does not infer identity constraints from `or`, funnel
step filters, properties or inequalities. Additional predicates remain in the
complete outer query. Queries without a necessary equality retain their usual
source.

The identity lookup returns candidate `(source_id,event_id)` pairs within the
authorized project and environment. Deduplication reads **every raw version** of
those pairs before selecting the winner. The outer query still applies the
requested identity, time, event, version and property filters. This prevents a
losing replay with a different identity or event time from replacing the actual
winner. Identity candidates are a conservative superset, not authoritative
identity resolution or a separate fact table.

New inserted parts include both projections after the DDL. Existing parts
remain queryable without projection data: ClickHouse reads their raw columns.
Absent, unmaterialized or disabled projections can increase reads and reach the
existing query budget; they do not mean there are no matching identities. No
projection is forced and no candidate set is truncated. The existing 2,000,000
rows, 256 MiB read, 512 MiB memory and 30 second execution budgets still apply.

For historical data, inspect `system.parts` and choose one bounded partition at
a time. Substitute the actual intended database and partition after checking
their size, available disk space and current mutation load:

```sql
SELECT partition_id, sum(rows) AS rows, sum(bytes_on_disk) AS bytes
FROM system.parts
WHERE database = currentDatabase() AND table = 'event_facts_raw' AND active
GROUP BY partition_id ORDER BY partition_id;

ALTER TABLE event_facts_raw
MATERIALIZE PROJECTION identity_uid IN PARTITION ID '202611'
SETTINGS mutations_sync=1;

ALTER TABLE event_facts_raw
MATERIALIZE PROJECTION identity_anonymous_id IN PARTITION ID '202611'
SETTINGS mutations_sync=1;
```

Wait for each operation to finish and inspect `system.mutations` for failures
before starting the next partition. Materialization is deliberately absent from
the migration. It creates projection data and consumes disk and background work;
it does not change the original event versions. Projection rollback can drop
the two projections explicitly; deploy the previous query worker first to avoid
an unexpected performance regression during rollback.

Use `EXPLAIN indexes=1,projections=1` on the candidate SELECT to confirm the
identity projection and its identity primary-key condition. Record final
`FORMAT JSON` statistics or completed `system.query_log` entries for actual
`read_rows` and `read_bytes`. The HTTP summary header can precede completion and
report partial reads. For controlled comparisons, disable the query condition
cache and data-read skip-index optimizations on both runs, then compare with
`optimize_use_projections=0`.

On ClickHouse 26.8.36.3, a 1,100,000-row isolated synthetic candidate lookup read
8,192 rows with the nullable UID projection and 1,100,000 with projections
disabled, returning the same eleven pairs. A regular projection sorted by
`ifNull(uid,'')` was considered usable but did not prune the identity key and
read all 1,100,000 rows; that expression projection was not shipped. The tagged
integration test repeats physical-plan and actual-read assertions for both
identity keys with 131,072 rows (8,192 projected versus 131,072 disabled), and
compares results with the original winner view before projection creation,
before/after historical materialization and with projections disabled.

These observations establish the access path on that synthetic distribution,
not production capacity. A frequent identity can still have a large candidate
set, and candidate lookup plus winner reconstruction must fit the same query
budgets. Queries remain bounded snapshots over observed ClickHouse parts;
concurrent ingestion and late earlier winners retain the existing eventual
coverage behavior. This change does not establish an ingestion watermark or
cross-query repeatable-read guarantee.

Run the focused real-dependency checks against explicit private test settings:

```sh
./deploy/dev-env run /tmp/evia-pipeline.env -- \
  go -C backend test -race -tags=integration \
  ./internal/analysis -run 'TestNecessaryIdentity|TestIdentityCandidateSource|TestClickHouseIdentity' -count=1
```

The tests create and delete a uniquely named disposable database; they require
the configured ClickHouse endpoint and credentials and fail if unavailable.
