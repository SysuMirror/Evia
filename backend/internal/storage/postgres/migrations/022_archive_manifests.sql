-- Version 022: durable archive manifests for facts removed by retention.
-- Before an enforced retention sweep deletes expired facts, the worker
-- materializes them into a bounded JSONL artifact and records one manifest.
-- The manifest is the recovery index for future backfills: it pins the scope,
-- cutoff, row count and artifact checksum, and carries the sweep id that
-- produced it. This table stores metadata only; event payloads live in the
-- artifact directory and never in PostgreSQL.
CREATE TABLE archive_manifests (
 id uuid PRIMARY KEY,
 project_id uuid NOT NULL REFERENCES projects(id),
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 -- Policy snapshot and the exclusive cutoff used when selecting expired facts.
 retention_days integer NOT NULL CHECK(retention_days BETWEEN 30 AND 3650),
 cutoff_at timestamptz NOT NULL,
 -- Actual event_time coverage observed inside the artifact; null while pending
 -- or when the archived scope contained zero rows.
 window_start_at timestamptz,
 window_end_at timestamptz,
 row_count bigint NOT NULL DEFAULT 0 CHECK(row_count BETWEEN 0 AND 9007199254740991),
 artifact_path text,
 artifact_sha256 text CHECK(artifact_sha256 IS NULL OR artifact_sha256 ~ '^[a-f0-9]{64}$'),
 artifact_bytes bigint CHECK(artifact_bytes IS NULL OR artifact_bytes >= 0),
 status text NOT NULL CHECK(status IN ('pending','materialized','failed')),
 error_code text,
 -- Correlation id of the triggering retention sweep. It is intentionally a
 -- loose uuid link rather than an FK: manifests are written inside the sweep
 -- before the sweep row is finalized, and recovery lookup never joins on it.
 sweep_id uuid,
 created_at timestamptz NOT NULL DEFAULT now(),
 completed_at timestamptz,
 CHECK((status='failed') = (error_code IS NOT NULL)),
 CHECK((status='materialized') = (artifact_path IS NOT NULL)),
 CHECK(status <> 'pending' OR (artifact_path IS NULL AND error_code IS NULL))
);
CREATE INDEX archive_manifests_scope_time
 ON archive_manifests(project_id, environment, created_at DESC, id DESC);
CREATE INDEX archive_manifests_sweep ON archive_manifests(sweep_id) WHERE sweep_id IS NOT NULL;
