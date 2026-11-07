-- Explicit additive operator migration; receipts never enter event fact tables.
CREATE TABLE pipeline_readiness
(
 project_id String,
 environment LowCardinality(String),
 release_id String,
 revision UInt64,
 manifest_hash FixedString(64),
 nonce UUID,
 issued_at DateTime64(9, 'UTC'),
 expires_at DateTime64(9, 'UTC'),
 capability LowCardinality(String),
 observed_at DateTime64(9, 'UTC')
)
ENGINE = MergeTree
PARTITION BY toDate(observed_at)
ORDER BY (nonce, observed_at)
TTL toDateTime(observed_at) + INTERVAL 1 DAY DELETE
SETTINGS fsync_after_insert = 1, fsync_part_directory = 1;
