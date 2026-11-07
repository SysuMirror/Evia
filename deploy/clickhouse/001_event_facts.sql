-- Version 001: explicit operator migration, never run on API startup.
-- Select the intended database with clickhouse-client --database before applying.
-- Local single-node durability; a replicated deployment needs a separately
-- reviewed topology and quorum policy rather than silently changing this table.
CREATE TABLE event_facts_raw
(
    project_id String,
    environment LowCardinality(String),
    source_id String,
    event_id String,
    event_key LowCardinality(String),
    source_kind LowCardinality(String),
    schema_version UInt64,
    release_revision UInt64,
    occurred_at DateTime64(3, 'UTC'),
    event_time DateTime64(3, 'UTC'),
    received_at DateTime64(9, 'UTC'),
    canonical_hash FixedString(64),
    uid Nullable(String),
    anonymous_id Nullable(String),
    session_id Nullable(String),
    tab_id Nullable(String),
    sequence Nullable(UInt64),
    page_view_id Nullable(String),
    source_page_view_id Nullable(String),
    navigation_id Nullable(String),
    operation_id Nullable(String),
    route_name Nullable(String),
    route_template Nullable(String),
    source_module Nullable(String),
    navigation_type Nullable(String),
    previous_page_view_id Nullable(String),
    sdk_name String,
    sdk_version String,
    context_json String,
    properties_json String
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(event_time)
ORDER BY (project_id, environment, event_key, toStartOfHour(event_time), source_id, event_id, received_at, canonical_hash)
SETTINGS fsync_after_insert = 1, fsync_part_directory = 1;

-- Exact source-scoped deduplication precedes every downstream event/property
-- filter and count. Background merges and insert dedup windows are not relied on.
CREATE VIEW event_facts AS
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
        argMin(tuple(event_key, schema_version, release_revision, occurred_at, event_time, received_at, canonical_hash, uid, anonymous_id, session_id, tab_id, sequence, page_view_id, source_page_view_id, navigation_id, operation_id, route_name, route_template, source_module, navigation_type, previous_page_view_id, sdk_name, sdk_version, context_json, properties_json, source_kind), tuple(received_at, canonical_hash)) AS fact
    FROM event_facts_raw
    GROUP BY project_id, environment, source_id, event_id
);
