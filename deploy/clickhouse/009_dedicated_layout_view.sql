-- Version 009: expose the version 008 dedicated-layout marker through the
-- deduplicated event_facts view. Apply after version 008 and before deploying
-- readers that select dedicated token columns. The existing 28 tuple slots,
-- their order, the winner weight and every existing view column are
-- unchanged; the marker is appended as fact.29 so current readers cannot
-- observe a difference.
--
-- The marker travels inside the SAME argMin tuple as properties_json, so it
-- always describes the same winning row as the authoritative document and
-- the property_tokens map. Legacy parts and writers that omit the version
-- 008 column contribute the UInt8 default 0, the explicit "no dedicated
-- materialization, read the document" signal. Dedicated token slot columns
-- are not part of this view yet: they are appended only when a later
-- migration adds the first approved catalog slots.
--
-- Safe to reapply; no table replacement, historical rewrite or key change.
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
