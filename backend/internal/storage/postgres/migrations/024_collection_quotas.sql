-- Version 024: per-project/environment collection quota overrides.
-- A row exists only after an owner publishes an override; absence means the
-- serve process applies its static default admission policy. Bounds mirror the
-- Redis budget engine validation so a persisted row can always be normalized.
CREATE TABLE collection_quotas (
 project_id uuid NOT NULL REFERENCES projects(id),
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 rate_per_second double precision NOT NULL CHECK(rate_per_second BETWEEN 0.001 AND 1000000),
 burst integer NOT NULL CHECK(burst BETWEEN 50 AND 1000000),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision BETWEEN 1 AND 9007199254740991),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(project_id, environment)
);

-- Owner-only human writes with the same replay semantics as retention policy
-- mutations: a reused key with a different body conflicts, an identical replay
-- returns the originally accepted quota snapshot.
CREATE TABLE collection_quota_mutations (
 project_id uuid NOT NULL REFERENCES projects(id),
 actor_id uuid NOT NULL REFERENCES accounts(id),
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 1 AND 128),
 request_hash text NOT NULL CHECK(length(request_hash)=64),
 response jsonb NOT NULL CHECK(jsonb_typeof(response)='object'),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(project_id, actor_id, idempotency_key)
);
CREATE INDEX collection_quota_mutations_actor
 ON collection_quota_mutations(actor_id, created_at);
