-- Version 018: auditable record of retention sweeps.
-- The retention worker evaluates every configured policy on an interval. Each
-- evaluation persists one row so operators can distinguish dry-run planning
-- (the safe default that only counts expired rows) from an enforced deletion,
-- and can reconstruct the cutoff and affected row count for every scope.
-- This table never stores event payloads and is independent of the ClickHouse
-- facts; erasure tombstones live in their own table and are never modified here.
CREATE TABLE retention_sweeps (
 id uuid PRIMARY KEY,
 project_id uuid NOT NULL REFERENCES projects(id),
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 -- Snapshot of the policy window used for this evaluation.
 retention_days integer NOT NULL CHECK(retention_days BETWEEN 30 AND 3650),
 cutoff_at timestamptz NOT NULL,
 -- dry_run=true means only candidate rows were counted; false means the bounded
 -- ClickHouse delete was issued and rows_expired reports the pre-delete count.
 dry_run boolean NOT NULL,
 rows_candidates bigint NOT NULL CHECK(rows_candidates BETWEEN 0 AND 9007199254740991),
 status text NOT NULL CHECK(status IN ('planned','succeeded','failed')),
 detail text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 completed_at timestamptz
);
CREATE INDEX retention_sweeps_scope_time
 ON retention_sweeps(project_id, environment, created_at DESC, id DESC);
-- Bounded recent-history lookup for operational review without a full scan.
CREATE INDEX retention_sweeps_created ON retention_sweeps(created_at DESC, id DESC);
