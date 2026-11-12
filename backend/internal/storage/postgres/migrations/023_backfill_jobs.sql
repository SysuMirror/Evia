-- Version 023: controllable backfill jobs that re-ingest archived JSONL facts.
-- A backfill references one archive manifest and streams its artifact back into
-- ClickHouse. The fact view deduplicates by event identity, so re-running a
-- backfill stays query-safe even when raw rows are reinserted. Jobs mirror the
-- export lifecycle vocabulary (queued/running/completed/failed/cancelled) with
-- lease-based claiming, idempotent creation and full audit attribution.
CREATE TABLE backfill_jobs (
 id uuid PRIMARY KEY,
 project_id uuid NOT NULL REFERENCES projects(id),
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 actor_id uuid NOT NULL REFERENCES accounts(id),
 -- The archive being re-ingested; restricted so a referenced manifest cannot
 -- be purged while backfill history still points at it.
 manifest_id uuid NOT NULL REFERENCES archive_manifests(id) ON DELETE RESTRICT,
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),
 state text NOT NULL DEFAULT 'queued'
  CHECK(state IN ('queued','running','completed','failed','cancelled')),
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 1 AND 128),
 request_hash text NOT NULL CHECK(request_hash ~ '^[a-f0-9]{64}$'),
 error_code text,
 rows_expected bigint NOT NULL DEFAULT 0 CHECK(rows_expected BETWEEN 0 AND 9007199254740991),
 rows_inserted bigint NOT NULL DEFAULT 0 CHECK(rows_inserted BETWEEN 0 AND 9007199254740991),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 5),
 lease_generation bigint NOT NULL DEFAULT 0,
 lease_owner text,
 lease_until timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 finished_at timestamptz,
 CHECK((state='failed') = (error_code IS NOT NULL)),
 CHECK(state IN ('queued','cancelled') OR lease_owner IS NOT NULL)
);
CREATE UNIQUE INDEX backfill_jobs_idempotency
 ON backfill_jobs(project_id,idempotency_key);
CREATE INDEX backfill_jobs_queue
 ON backfill_jobs(state,created_at,id)
 WHERE state IN ('queued','running');
CREATE INDEX backfill_jobs_project_time
 ON backfill_jobs(project_id,created_at,id);
CREATE INDEX backfill_jobs_manifest ON backfill_jobs(manifest_id);
