CREATE TABLE analysis_query_jobs (
 id uuid PRIMARY KEY,
 project_id uuid NOT NULL REFERENCES projects(id),
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 actor_id uuid NOT NULL REFERENCES accounts(id),
 kind text NOT NULL CHECK(kind IN ('events','trend','journey','funnel')),
 request jsonb NOT NULL,
 state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','running','succeeded','failed','canceled','expired')),
 created_at timestamptz NOT NULL DEFAULT now(),
 queued_deadline timestamptz NOT NULL DEFAULT now()+interval '10 minutes',
 expires_at timestamptz NOT NULL DEFAULT now()+interval '24 hours',
 finished_at timestamptz,
 result_expires_at timestamptz,
 result_json jsonb,
 error_json jsonb,
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 3),
 lease_generation bigint NOT NULL DEFAULT 0,
 lease_owner text,
 lease_until timestamptz,
 CHECK(state<>'running' OR (lease_owner IS NOT NULL AND lease_until IS NOT NULL)),
 CHECK(state<>'succeeded' OR result_json IS NOT NULL),
 CHECK(result_json IS NULL OR state='succeeded'),
 CHECK(result_json IS NULL OR octet_length(result_json::text)<=2097152)
);
CREATE INDEX analysis_queries_queue ON analysis_query_jobs(state,created_at,id) WHERE state IN ('queued','running');
CREATE INDEX analysis_queries_project_state ON analysis_query_jobs(project_id,state,lease_until);
CREATE INDEX analysis_queries_expiry ON analysis_query_jobs(expires_at,result_expires_at);
