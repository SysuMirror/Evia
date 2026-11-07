-- Version 003: apply explicitly BEFORE deploying the precise timestamp sink.
-- Version 001 remains immutable. Its event_time participates in partition/sort
-- keys, so ClickHouse forbids changing that column's type in place. Retain those
-- millisecond columns for layout compatibility and add two precise payloads.
-- Old parts and old writers use defaults that preserve their actual millisecond
-- values; this does not invent historical sub-millisecond precision. New writers
-- supply all four fields. Rolling back the writer remains compatible. Nullable
-- precise payloads and view coalesce also support JSONEachRow profiles that
-- substitute type defaults for omitted old-writer fields, instead of evaluating
-- their DEFAULT expressions. Public view timestamps stay non-null DateTime64(9).
-- If an old lossy row and its precise replay share receipt/hash, timestamp ties
-- choose the stored millisecond value deterministically; replay does not invent
-- historical precision. Receipt time and canonical hash retain first priority.
-- The additional columns trade storage for a non-destructive upgrade. This script
-- is safe to reapply; no table replacement, destructive copy or key rewrite occurs.
-- Precise timestamps support 1900-01-01 through 2262-04-11T23:47:16.854775807Z.
-- Preflight scans legacy timestamps before any DDL. Unsupported historical dates
-- require an explicit operator decision rather than silently overflowing nanos.
SELECT throwIf(countIf(
 occurred_at > toDateTime64('2262-04-11 23:47:16.854', 3, 'UTC')
 OR event_time > toDateTime64('2262-04-11 23:47:16.854', 3, 'UTC')
) > 0, 'legacy event timestamp exceeds DateTime64(9) range') FROM event_facts_raw;

ALTER TABLE event_facts_raw
 ADD COLUMN IF NOT EXISTS occurred_at_precise Nullable(DateTime64(9, 'UTC')) DEFAULT toDateTime64(occurred_at, 9, 'UTC') AFTER occurred_at,
 ADD COLUMN IF NOT EXISTS event_time_precise Nullable(DateTime64(9, 'UTC')) DEFAULT toDateTime64(event_time, 9, 'UTC') AFTER event_time;

CREATE OR REPLACE VIEW event_facts AS
SELECT
    project_id,
    environment,
    source_id,
    event_id,
    fact.1 AS event_key,
    fact.2 AS schema_version,
    fact.3 AS release_revision,
    fact.4 AS occurred_at,
    fact.5 AS event_time,
    fact.6 AS received_at,
    fact.7 AS canonical_hash,
    fact.8 AS uid,
    fact.9 AS anonymous_id,
    fact.10 AS session_id,
    fact.11 AS tab_id,
    fact.12 AS sequence,
    fact.13 AS page_view_id,
    fact.14 AS source_page_view_id,
    fact.15 AS navigation_id,
    fact.16 AS operation_id,
    fact.17 AS route_name,
    fact.18 AS route_template,
    fact.19 AS source_module,
    fact.20 AS navigation_type,
    fact.21 AS previous_page_view_id,
    fact.22 AS sdk_name,
    fact.23 AS sdk_version,
    fact.24 AS context_json,
    fact.25 AS properties_json,
    fact.26 AS source_kind
FROM
(
    SELECT
        project_id,
        environment,
        source_id,
        event_id,
        argMin(tuple(event_key, schema_version, release_revision, coalesce(occurred_at_precise, toDateTime64(occurred_at, 9, 'UTC')), coalesce(event_time_precise, toDateTime64(event_time, 9, 'UTC')), received_at, canonical_hash, uid, anonymous_id, session_id, tab_id, sequence, page_view_id, source_page_view_id, navigation_id, operation_id, route_name, route_template, source_module, navigation_type, previous_page_view_id, sdk_name, sdk_version, context_json, properties_json, source_kind), tuple(received_at, canonical_hash, coalesce(occurred_at_precise, toDateTime64(occurred_at, 9, 'UTC')), coalesce(event_time_precise, toDateTime64(event_time, 9, 'UTC')))) AS fact
    FROM event_facts_raw
    GROUP BY project_id, environment, source_id, event_id
);
