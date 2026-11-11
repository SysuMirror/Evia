-- Version 019: bounded data export jobs and one-time download grants.
-- An export runs the same bounded analysis request contract as the ad-hoc
-- query API and materializes its result as a versioned JSON artifact. Jobs are
-- owner-only for human sessions; machines need the explicit data.export grant
-- (migration 020). Download capability is never a session: a one-time hashed
-- grant token redeems the artifact exactly once inside its expiry window.
CREATE TABLE export_jobs (
 id uuid PRIMARY KEY,
 project_id uuid NOT NULL REFERENCES projects(id),
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 actor_id uuid NOT NULL REFERENCES accounts(id),
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),
 format text NOT NULL DEFAULT 'json' CHECK(format IN ('json')),
 request_json jsonb NOT NULL,
 state text NOT NULL DEFAULT 'queued'
  CHECK(state IN ('queued','running','completed','failed','cancelled')),
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 1 AND 128),
 request_hash text NOT NULL CHECK(request_hash ~ '^[a-f0-9]{64}$'),
 error_code text,
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 5),
 lease_generation bigint NOT NULL DEFAULT 0,
 lease_owner text,
 lease_until timestamptz,
 artifact_path text,
 artifact_sha256 text CHECK(artifact_sha256 IS NULL OR artifact_sha256 ~ '^[a-f0-9]{64}$'),
 artifact_bytes bigint CHECK(artifact_bytes IS NULL OR artifact_bytes >= 0),
 result_rows integer CHECK(result_rows IS NULL OR result_rows >= 0),
 expires_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 finished_at timestamptz,
 CHECK((state='failed') = (error_code IS NOT NULL)),
 CHECK(state IN ('queued','cancelled') OR lease_owner IS NOT NULL),
 CHECK(artifact_path IS NULL OR state='completed')
);
-- Idempotent creation replays the accepted snapshot per actor.
CREATE UNIQUE INDEX export_jobs_idempotency
 ON export_jobs(project_id,actor_id,idempotency_key);
-- Worker queue index mirrors the erasure/query claim pattern.
CREATE INDEX export_jobs_queue
 ON export_jobs(state,created_at,id)
 WHERE state IN ('queued','running');
CREATE INDEX export_jobs_project_time
 ON export_jobs(project_id,created_at,id);

CREATE TABLE export_download_grants (
 token_hash text PRIMARY KEY CHECK(token_hash ~ '^[a-f0-9]{64}$'),
 job_id uuid NOT NULL REFERENCES export_jobs(id) ON DELETE CASCADE,
 project_id uuid NOT NULL REFERENCES projects(id),
 actor_id uuid NOT NULL REFERENCES accounts(id),
 expires_at timestamptz NOT NULL,
 redeemed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
-- Only active grants need redemption lookups; the partial index stays small.
CREATE INDEX export_download_grants_active
 ON export_download_grants(expires_at) WHERE redeemed_at IS NULL;
CREATE INDEX export_download_grants_job ON export_download_grants(job_id);
