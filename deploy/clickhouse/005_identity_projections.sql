-- Version 005: optional access paths for exact raw identity candidates.
-- This metadata setting permits nullable identity keys in projections. The
-- base table's ORDER BY and partition layout remain unchanged.
ALTER TABLE event_facts_raw MODIFY SETTING allow_nullable_key = 1;

-- New inserts build these projections. Existing parts stay readable without
-- them; historical materialization is an explicit, bounded operator action.
-- Keep ordinary SELECTs so missing projection parts use the raw-table fallback.
ALTER TABLE event_facts_raw ADD PROJECTION IF NOT EXISTS identity_uid
(
    SELECT project_id, environment, uid, source_id, event_id
    ORDER BY (project_id, environment, uid, source_id, event_id)
);

ALTER TABLE event_facts_raw ADD PROJECTION IF NOT EXISTS identity_anonymous_id
(
    SELECT project_id, environment, anonymous_id, source_id, event_id
    ORDER BY (project_id, environment, anonymous_id, source_id, event_id)
);
