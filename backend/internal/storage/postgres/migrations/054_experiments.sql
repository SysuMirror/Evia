-- Version 054: add experiment analysis kind to query jobs and saved analyses,
-- and create project-scoped experiment configuration records for deterministic
-- client/server bucketing delivered via signed public config.
ALTER TABLE analysis_query_jobs DROP CONSTRAINT analysis_query_jobs_kind_check;
ALTER TABLE analysis_query_jobs ADD CONSTRAINT analysis_query_jobs_kind_check
 CHECK(kind IN ('events','trend','journey','funnel','retention','segment','path','attribution','heatmap','experiment'));

ALTER TABLE saved_analyses DROP CONSTRAINT saved_analyses_kind_check;
ALTER TABLE saved_analyses ADD CONSTRAINT saved_analyses_kind_check
 CHECK(kind IN ('trend','funnel','retention','segment','path','attribution','experiment'));

CREATE TABLE experiments (
 id uuid PRIMARY KEY,
 project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 experiment_key text NOT NULL CHECK(experiment_key ~ '^[a-z][a-z0-9_]{1,63}$'),
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 128),
 description text NOT NULL DEFAULT '' CHECK(length(description) <= 2000),
 status text NOT NULL CHECK(status IN ('draft','running','paused','completed')),
 salt text NOT NULL CHECK(length(salt) BETWEEN 1 AND 64),
 control_variant text NOT NULL CHECK(control_variant ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
 exposure_event_key text NOT NULL DEFAULT 'experiment_exposed' CHECK(exposure_event_key ~ '^[a-z][a-z0-9_]{1,63}$'),
 variants_json jsonb NOT NULL,
 overrides_json jsonb NOT NULL DEFAULT '{}'::jsonb,
 version bigint NOT NULL DEFAULT 1 CHECK(version >= 1),
 started_at timestamptz,
 ended_at timestamptz,
 created_by uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(project_id, environment, experiment_key)
);

CREATE INDEX experiments_project_env_status ON experiments (project_id, environment, status, experiment_key);
