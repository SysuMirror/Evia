-- Version 004: explicit additive read-layout migration, after version 003.
-- Apply before deploying readers that use these columns. Original payloads,
-- sort/partition keys and the event_facts view remain the semantic reference.
-- Guard future inserts before checking history, so concurrent inserts cannot
-- introduce malformed hashes between preflight and compact-column creation.
-- Lowercase hexadecimal lexical order equals decoded-byte lexical order.
ALTER TABLE event_facts_raw
 ADD CONSTRAINT IF NOT EXISTS canonical_hash_lower_hex CHECK match(canonical_hash, '^[0-9a-f]{64}$');

SELECT throwIf(countIf(NOT match(canonical_hash, '^[0-9a-f]{64}$')) > 0,
 'canonical hash must contain exactly 64 lowercase hexadecimal bytes') FROM event_facts_raw;

-- Server-computed columns keep old and new writers on the same insert path.
-- Missing historical columns are computed from the original stored payloads
-- on read. This is correct before backfill, but does not establish scan savings.
-- No historical parts are rewritten automatically. Follow the bounded operator
-- procedure in docs/runbooks/compact-winner-upgrade.md to materialize them.
ALTER TABLE event_facts_raw
 ADD COLUMN IF NOT EXISTS canonical_hash_bytes FixedString(32) MATERIALIZED toFixedString(unhex(canonical_hash), 32),
 ADD COLUMN IF NOT EXISTS occurred_at_effective DateTime64(9, 'UTC') MATERIALIZED coalesce(occurred_at_precise, toDateTime64(occurred_at, 9, 'UTC')),
 ADD COLUMN IF NOT EXISTS event_time_effective DateTime64(9, 'UTC') MATERIALIZED coalesce(event_time_precise, toDateTime64(event_time, 9, 'UTC'));
