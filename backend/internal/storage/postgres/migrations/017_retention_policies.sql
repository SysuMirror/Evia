-- Version 017: per-environment event retention policy (configuration layer).
-- A row exists only after an owner defines a retention window; absence means
-- no automated age-based deletion is configured, so facts are retained
-- indefinitely until a policy is published. This migration stores the durable
-- policy and its audit/idempotency records only. The enforcement component that
-- applies the window to ClickHouse is a separate, explicitly audited worker and
-- must never remove erasure tombstones or weaken the resurrection barrier.
CREATE TABLE retention_policies (
 project_id uuid NOT NULL REFERENCES projects(id),
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 retention_days integer NOT NULL CHECK(retention_days BETWEEN 30 AND 3650),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision BETWEEN 1 AND 9007199254740991),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(project_id, environment)
);

-- Owner-only human writes, replayed per actor exactly like governance
-- mutations. A reused key with a different request body conflicts; an identical
-- replay returns the originally accepted policy snapshot.
CREATE TABLE retention_policy_mutations (
 project_id uuid NOT NULL REFERENCES projects(id),
 actor_id uuid NOT NULL REFERENCES accounts(id),
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 1 AND 128),
 request_hash text NOT NULL CHECK(length(request_hash)=64),
 response jsonb NOT NULL CHECK(jsonb_typeof(response)='object'),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(project_id, actor_id, idempotency_key)
);
CREATE INDEX retention_policy_mutations_actor
 ON retention_policy_mutations(actor_id, created_at);
