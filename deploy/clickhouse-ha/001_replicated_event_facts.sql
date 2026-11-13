-- HA drill only: replicated counterpart of the production event_facts schema.
--
-- This file is never applied by the production migration harness (which globs
-- deploy/clickhouse/*.sql, not this directory). The column set mirrors every
-- insert-facing column added by migrations 001, 003, 006 and 008, in the same
-- order and with the same types, so the production pipeline sink can insert
-- its current JSONEachRow payload without any change; 004's MATERIALIZED helper
-- columns and 005's projections are intentionally omitted because they never
-- appear in insert payloads or in the read view. Only the engine differs,
-- exchanging MergeTree for ReplicatedMergeTree coordinated by the ClickHouse
-- Keeper quorum. Apply the same statement on every node: Keeper deduplicates
-- the replica registration through the shared zk path.
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
    occurred_at_precise Nullable(DateTime64(9, 'UTC')),
    event_time DateTime64(3, 'UTC'),
    event_time_precise Nullable(DateTime64(9, 'UTC')),
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
    properties_json String,
    property_layout_version UInt8 DEFAULT 0,
    property_tokens Map(String, String) DEFAULT map(),
    dedicated_layout_version UInt8 DEFAULT 0
)
ENGINE = ReplicatedMergeTree('/clickhouse/tables/{shard}/event_facts_raw', '{replica}')
PARTITION BY toYYYYMM(event_time)
ORDER BY (project_id, environment, event_key, toStartOfHour(event_time), source_id, event_id, received_at, canonical_hash)
SETTINGS fsync_after_insert = 1, fsync_part_directory = 1;

-- Views are local objects, so each node defines the same read view over its
-- own replicated table. The body matches migration 009, the current production
-- view, including the 29-slot argMin tuple with property layout fields.
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
    fact.26 AS source_kind,
    fact.27 AS property_layout_version,
    fact.28 AS property_tokens,
    fact.29 AS dedicated_layout_version
FROM
(
    SELECT
        project_id,
        environment,
        source_id,
        event_id,
        argMin(tuple(event_key, schema_version, release_revision, coalesce(occurred_at_precise, toDateTime64(occurred_at, 9, 'UTC')), coalesce(event_time_precise, toDateTime64(event_time, 9, 'UTC')), received_at, canonical_hash, uid, anonymous_id, session_id, tab_id, sequence, page_view_id, source_page_view_id, navigation_id, operation_id, route_name, route_template, source_module, navigation_type, previous_page_view_id, sdk_name, sdk_version, context_json, properties_json, source_kind, property_layout_version, property_tokens, dedicated_layout_version), tuple(received_at, canonical_hash, coalesce(occurred_at_precise, toDateTime64(occurred_at, 9, 'UTC')), coalesce(event_time_precise, toDateTime64(event_time, 9, 'UTC')))) AS fact
    FROM event_facts_raw
    GROUP BY project_id, environment, source_id, event_id
);
