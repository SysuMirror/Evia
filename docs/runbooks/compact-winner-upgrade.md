# Compact winner read layout

Migration `004_compact_winner_columns.sql` adds server-computed columns to the existing raw table. Apply migrations 001–003 first, then apply 004 explicitly to the intended database before deploying query readers that use the compact columns. API startup does not apply migrations. The public `event_facts` view remains the reference for winner semantics.

The winner order remains receipt time, canonical hash, effective occurred time and effective event time. Each hash is exactly 64 lowercase hexadecimal bytes. Decoding two hexadecimal digits into one byte preserves lexical order, including zero and high bytes; no hash prefix, integer conversion or lossy identity hash is used. Effective timestamps use the same nullable precise timestamp and legacy millisecond fallback as migration 003. Winner payloads still use one tuple-valued `argMin`, preserving null fields and conflicting replay behavior.

The migration first installs a constraint for future writes and then checks historical hashes. This order closes the concurrent insert gap. If preflight fails, stop the migration and investigate the original malformed rows under the existing recovery policy. Do not lowercase, truncate, discard or replace historical hashes to pass preflight: doing so can change winner order. The future-write constraint remains installed after a failed preflight. Reapplying the migration is supported after the cause is resolved.

The three added columns are `MATERIALIZED` expressions computed by ClickHouse during the original raw-table insert. Writers continue to supply the original fields; no second table or separate application write must succeed. Old writers that omit precise timestamps, including profiles that substitute null for omitted nullable fields, retain the legacy fallback. Original payloads, partition keys and sort keys are retained. Reader rollback uses the original view and columns; leave the additive columns in place during rollback.

Historical parts initially compute missing compact columns when read. Results are correct in that state, but these reads still need the original hash and timestamp columns. Merely applying the migration does not establish lower scan bytes. The migration deliberately does not rewrite historical parts or launch a full-table mutation.

Before backfill, inspect active partition sizes, unfinished mutations and free disk space on the intended database. Preserve the deployment's disk reserve and capacity for ordinary ingestion and merges. Use the authenticated operator client with the intended database selected; the following queries contain no credentials:

```sql
SELECT partition_id, sum(rows) AS rows, sum(bytes_on_disk) AS bytes_on_disk
FROM system.parts
WHERE database = currentDatabase() AND table = 'event_facts_raw' AND active
GROUP BY partition_id ORDER BY partition_id;

SELECT mutation_id, command, parts_to_do, is_done, latest_fail_reason
FROM system.mutations
WHERE database = currentDatabase() AND table = 'event_facts_raw' AND NOT is_done;

SELECT name, free_space, unreserved_space, keep_free_space FROM system.disks;
```

Select one observed partition ID at a time. For example, `202611` is appropriate only if the preceding inspection identifies that month in the intended database. Run one command, inspect its mutation until completion, and reassess disk space and ingest backlog before continuing to the next column or partition:

```sql
ALTER TABLE event_facts_raw MATERIALIZE COLUMN canonical_hash_bytes
IN PARTITION ID '202611';

SELECT mutation_id, command, parts_to_do, is_done, latest_fail_reason
FROM system.mutations
WHERE database = currentDatabase() AND table = 'event_facts_raw'
ORDER BY create_time DESC;

ALTER TABLE event_facts_raw MATERIALIZE COLUMN occurred_at_effective
IN PARTITION ID '202611';

ALTER TABLE event_facts_raw MATERIALIZE COLUMN event_time_effective
IN PARTITION ID '202611';
```

`ALTER` acknowledgement means the mutation was accepted, not that every historical part has been rewritten. Wait for the corresponding mutation's `is_done = 1`; investigate failures before proceeding. Do not start all three commands concurrently. Mutation rewrites need temporary disk space and compete with ingestion and ordinary merges. If a single monthly partition cannot be rewritten within the deployment's operational budget, defer backfill and retain the original read cost rather than launch an unbounded rewrite. New inserts already contain compact values.

After each selected partition completes all three mutations, compare the compact values with their original expressions. Keep this verification bounded to the completed partition; a whole-history verification can itself consume substantial resources:

```sql
SELECT count() AS checked_rows, countIf(
    canonical_hash_bytes != unhex(canonical_hash)
    OR occurred_at_effective != coalesce(occurred_at_precise, toDateTime64(occurred_at, 9, 'UTC'))
    OR event_time_effective != coalesce(event_time_precise, toDateTime64(event_time, 9, 'UTC'))
) AS mismatches
FROM event_facts_raw
WHERE _partition_id = '202611';
```

Require zero mismatches. Compare public query results with the unchanged view on a bounded replay fixture, including null winners and receipt/hash/timestamp ties. Measure `read_rows`, `read_bytes`, peak memory and execution time on the same dataset after materialization; preserve the configured row, scan, memory and timeout budgets. Compact columns reduce bytes needed for hash and timestamp ordering, but do not establish that every million-event query fits the default budget. High-cardinality winner aggregation and property payloads remain separate costs.
