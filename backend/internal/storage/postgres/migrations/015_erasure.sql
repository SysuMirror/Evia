-- Version 015: data erasure jobs and the durable resurrection barrier.
-- A job erases exactly one identity (uid or anonymous_id) in one
-- project/environment. The tombstone is written before any ClickHouse delete
-- so a Kafka replay or source retry cannot resurrect erased facts; the write
-- worker consults tombstones per batch and fails closed when the check fails.
-- Tombstones are permanent rows even if the job later fails, keeping the
-- barrier conservative until an operator retries the job.
CREATE TABLE erasure_jobs (
 id uuid PRIMARY KEY,
 project_id uuid NOT NULL REFERENCES projects(id),
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 actor_id uuid NOT NULL REFERENCES accounts(id),
 subject_kind text NOT NULL CHECK(subject_kind IN ('uid','anonymous_id')),
 subject_value text NOT NULL CHECK(length(subject_value) BETWEEN 1 AND 128),
 reason text NOT NULL CHECK(length(reason) BETWEEN 1 AND 1024),
 state text NOT NULL DEFAULT 'queued'
  CHECK(state IN ('queued','hiding','purging','rebuilding','completed','failed')),
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 1 AND 128),
 request_hash text NOT NULL CHECK(request_hash ~ '^[a-f0-9]{64}$'),
 backup_expiry_at timestamptz,
 error_code text,
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 5),
 lease_generation bigint NOT NULL DEFAULT 0,
 lease_owner text,
 lease_until timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 finished_at timestamptz,
 CHECK(backup_expiry_at IS NULL OR backup_expiry_at>created_at),
 CHECK(state IN ('queued','completed','failed')
   OR (lease_owner IS NOT NULL AND lease_until IS NOT NULL)),
 CHECK((state='failed') = (error_code IS NOT NULL))
);
-- Idempotent job creation replays the accepted snapshot per actor.
CREATE UNIQUE INDEX erasure_jobs_idempotency
 ON erasure_jobs(project_id,actor_id,idempotency_key);
-- At most one in-flight erasure per target identity. Terminal jobs leave the
-- index, so a retry after failure or a repeated request remains possible.
CREATE UNIQUE INDEX erasure_jobs_active_target
 ON erasure_jobs(project_id,environment,subject_kind,subject_value)
 WHERE state IN ('queued','hiding','purging','rebuilding');
CREATE INDEX erasure_jobs_queue
 ON erasure_jobs(state,created_at,id)
 WHERE state IN ('queued','hiding','purging','rebuilding');
CREATE INDEX erasure_jobs_project_time
 ON erasure_jobs(project_id,created_at,id);

CREATE TABLE erasure_tombstones (
 project_id uuid NOT NULL,
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 subject_kind text NOT NULL CHECK(subject_kind IN ('uid','anonymous_id')),
 subject_value text NOT NULL CHECK(length(subject_value) BETWEEN 1 AND 128),
 job_id uuid NOT NULL REFERENCES erasure_jobs(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(project_id,environment,subject_kind,subject_value)
);
CREATE INDEX erasure_tombstones_job ON erasure_tombstones(job_id);
