-- Retry metadata also records the exact immutable source of a saved execution.
-- Query cleanup may remove the job while its seven-day retry marker survives.
ALTER TABLE analysis_query_jobs ADD CONSTRAINT analysis_query_jobs_scoped_id
 UNIQUE(project_id,environment,id);
CREATE TABLE saved_analysis_query_requests (
 actor_id uuid NOT NULL REFERENCES accounts(id),
 project_id uuid NOT NULL REFERENCES projects(id),
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 analysis_id uuid NOT NULL,
 version bigint NOT NULL CHECK(version BETWEEN 1 AND 9007199254740991),
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 1 AND 200),
 request_hash text NOT NULL CHECK(request_hash ~ '^[a-f0-9]{64}$'),
 query_id uuid,
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL DEFAULT now()+interval '7 days',
 PRIMARY KEY(actor_id,project_id,environment,analysis_id,idempotency_key),
 FOREIGN KEY(project_id,environment,analysis_id,version)
  REFERENCES saved_analysis_versions(project_id,environment,analysis_id,version),
 FOREIGN KEY(project_id,environment,query_id)
  REFERENCES analysis_query_jobs(project_id,environment,id) ON DELETE SET NULL(query_id),
 CHECK(expires_at>created_at)
);
CREATE INDEX saved_analysis_query_requests_expiry
 ON saved_analysis_query_requests(project_id,expires_at);
CREATE INDEX saved_analysis_query_requests_job
 ON saved_analysis_query_requests(project_id,environment,query_id) WHERE query_id IS NOT NULL;
