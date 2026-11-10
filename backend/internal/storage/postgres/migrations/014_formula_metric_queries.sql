-- Composite formula metric executions reuse the shared analysis query jobs and
-- budget. This table only adds the idempotent, version-pinned provenance that
-- maps a formula run to its durable query job, mirroring metric_query_requests.
CREATE TABLE formula_metric_query_requests (
 actor_id uuid NOT NULL REFERENCES principals(id),
 project_id uuid NOT NULL,
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 metric_id uuid NOT NULL,
 version bigint NOT NULL CHECK(version BETWEEN 1 AND 9007199254740991),
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 1 AND 128),
 request_hash text NOT NULL CHECK(request_hash ~ '^[a-f0-9]{64}$'),
 query_id uuid,
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL DEFAULT now()+interval '7 days',
 PRIMARY KEY(actor_id,project_id,environment,metric_id,idempotency_key),
 FOREIGN KEY(project_id,environment,metric_id,version)
  REFERENCES formula_metric_versions(project_id,environment,metric_id,version),
 FOREIGN KEY(project_id,environment,query_id)
  REFERENCES analysis_query_jobs(project_id,environment,id) ON DELETE SET NULL(query_id),
 CHECK(expires_at>created_at)
);
CREATE INDEX formula_metric_query_requests_expiry
 ON formula_metric_query_requests(project_id,expires_at);
CREATE INDEX formula_metric_query_requests_job
 ON formula_metric_query_requests(project_id,environment,query_id) WHERE query_id IS NOT NULL;
CREATE TRIGGER formula_metric_query_requests_immutable
 BEFORE UPDATE OR DELETE ON formula_metric_query_requests
 FOR EACH ROW EXECUTE FUNCTION catalog_reject_immutable_change();
