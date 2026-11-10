-- Version 007: expose the version 006 property layout through the deduplicated
-- event_facts view. Apply after version 006 and before deploying readers that
-- select the two new columns. The original 26 tuple slots, their order, the
-- winner weight and every existing view column are unchanged; the layout
-- version and token map are appended as fact.27 and fact.28 so current readers
-- cannot observe a difference.
--
-- Both new slots travel inside the SAME argMin tuple as properties_json. The
-- deduplicated layout therefore always belongs to the same winning row as the
-- authoritative document of that fact; independent aggregates could pair the
-- document of one replay with the layout of another. Legacy parts and writers
-- that omitted the version 006 columns contribute the type defaults (version
-- 0, empty map), which is the explicit "read the authoritative document"
-- signal for the future reader switch.
--
-- Safe to reapply; no table replacement, historical rewrite or key change.
-- The token map duplicates property payload bytes in aggregate state, so the
-- later reader switch still requires physical-read verification before any
-- capacity claim.
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
    fact.28 AS property_tokens
FROM
(
    SELECT
        project_id,
        environment,
        source_id,
        event_id,
        argMin(tuple(event_key, schema_version, release_revision, coalesce(occurred_at_precise, toDateTime64(occurred_at, 9, 'UTC')), coalesce(event_time_precise, toDateTime64(event_time, 9, 'UTC')), received_at, canonical_hash, uid, anonymous_id, session_id, tab_id, sequence, page_view_id, source_page_view_id, navigation_id, operation_id, route_name, route_template, source_module, navigation_type, previous_page_view_id, sdk_name, sdk_version, context_json, properties_json, source_kind, property_layout_version, property_tokens), tuple(received_at, canonical_hash, coalesce(occurred_at_precise, toDateTime64(occurred_at, 9, 'UTC')), coalesce(event_time_precise, toDateTime64(event_time, 9, 'UTC')))) AS fact
    FROM event_facts_raw
    GROUP BY project_id, environment, source_id, event_id
);
