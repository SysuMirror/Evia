-- Mutation 040: dashboard composite formula metric widgets.
--
-- A dashboard metric widget may now pin either an atomic metric_versions row
-- or a composite formula_metric_versions row, selected by metric_ref.kind in
-- the definition JSON. The witness tables keep their generic identity columns
-- and gain a third nullable foreign-key target; exactly one of analysis,
-- atomic metric or composite formula must be populated on every row. Existing
-- analysis/atomic rows stay untouched because composite foreign keys use MATCH
-- SIMPLE and skip any row containing a NULL. No backfill is needed: the new
-- columns are nullable and historical rows already satisfy the new CHECK.

-- 1. Publication witnesses: replace the atomic-only foreign key with a
-- dual metric target (atomic metric_versions + composite formula_metric_versions).
DO $$
DECLARE
 witness_fk text;
BEGIN
 SELECT conname INTO witness_fk
  FROM pg_constraint
  WHERE conrelid = 'dashboard_dependencies'::regclass
   AND contype = 'f'
   AND confrelid = 'metric_versions'::regclass;
 IF witness_fk IS NULL THEN
  RAISE EXCEPTION 'dashboard dependency atomic metric foreign key not found' USING ERRCODE='23514';
 END IF;
 EXECUTE 'ALTER TABLE dashboard_dependencies DROP CONSTRAINT ' || quote_ident(witness_fk);
END
$$;

ALTER TABLE dashboard_dependencies
 ADD COLUMN formula_metric_id uuid,
 ADD COLUMN formula_metric_version bigint;

ALTER TABLE dashboard_dependencies
 ADD CONSTRAINT dashboard_dependencies_metric_ref_fkey
  FOREIGN KEY(project_id,environment,metric_id,metric_version)
  REFERENCES metric_versions(project_id,environment,metric_id,version),
 ADD CONSTRAINT dashboard_dependencies_formula_ref_fkey
  FOREIGN KEY(project_id,environment,formula_metric_id,formula_metric_version)
  REFERENCES formula_metric_versions(project_id,environment,metric_id,version),
 ADD CONSTRAINT dashboard_dependencies_formula_unique
  UNIQUE(project_id,environment,dashboard_id,version,widget_id,formula_metric_id,formula_metric_version);

ALTER TABLE dashboard_dependencies DROP CONSTRAINT dashboard_dependencies_source_sound;
ALTER TABLE dashboard_dependencies ADD CONSTRAINT dashboard_dependencies_source_sound CHECK(
 (
  analysis_id IS NOT NULL AND analysis_version IS NOT NULL
  AND metric_id IS NULL AND metric_version IS NULL
  AND formula_metric_id IS NULL AND formula_metric_version IS NULL
 ) OR (
  metric_id IS NOT NULL AND metric_version IS NOT NULL
  AND analysis_id IS NULL AND analysis_version IS NULL
  AND formula_metric_id IS NULL AND formula_metric_version IS NULL
 ) OR (
  formula_metric_id IS NOT NULL AND formula_metric_version IS NOT NULL
  AND analysis_id IS NULL AND analysis_version IS NULL
  AND metric_id IS NULL AND metric_version IS NULL
 )
);

-- 2. Run widgets mirror the same triple-target shape. The metric foreign key
-- now points at the matching dependency witness columns for either target.
DO $$
DECLARE
 run_fk text;
BEGIN
 SELECT conname INTO run_fk
  FROM pg_constraint
  WHERE conrelid = 'dashboard_run_widgets'::regclass
   AND contype = 'f'
   AND confrelid = 'dashboard_dependencies'::regclass
   AND pg_get_constraintdef(oid) LIKE '%metric_id%';
 IF run_fk IS NULL THEN
  RAISE EXCEPTION 'dashboard run widget metric foreign key not found' USING ERRCODE='23514';
 END IF;
 EXECUTE 'ALTER TABLE dashboard_run_widgets DROP CONSTRAINT ' || quote_ident(run_fk);
END
$$;

ALTER TABLE dashboard_run_widgets
 ADD COLUMN formula_metric_id uuid,
 ADD COLUMN formula_metric_version bigint;

ALTER TABLE dashboard_run_widgets
 ADD CONSTRAINT dashboard_run_widgets_metric_ref_fkey
  FOREIGN KEY(project_id,environment,dashboard_id,version,widget_id,metric_id,metric_version)
  REFERENCES dashboard_dependencies(project_id,environment,dashboard_id,version,widget_id,metric_id,metric_version),
 ADD CONSTRAINT dashboard_run_widgets_formula_ref_fkey
  FOREIGN KEY(project_id,environment,dashboard_id,version,widget_id,formula_metric_id,formula_metric_version)
  REFERENCES dashboard_dependencies(project_id,environment,dashboard_id,version,widget_id,formula_metric_id,formula_metric_version);

ALTER TABLE dashboard_run_widgets DROP CONSTRAINT dashboard_run_widgets_source_sound;
ALTER TABLE dashboard_run_widgets ADD CONSTRAINT dashboard_run_widgets_source_sound CHECK(
 (
  analysis_id IS NOT NULL AND analysis_version IS NOT NULL
  AND metric_id IS NULL AND metric_version IS NULL
  AND formula_metric_id IS NULL AND formula_metric_version IS NULL
 ) OR (
  metric_id IS NOT NULL AND metric_version IS NOT NULL
  AND analysis_id IS NULL AND analysis_version IS NULL
  AND formula_metric_id IS NULL AND formula_metric_version IS NULL
 ) OR (
  formula_metric_id IS NOT NULL AND formula_metric_version IS NOT NULL
  AND analysis_id IS NULL AND analysis_version IS NULL
  AND metric_id IS NULL AND metric_version IS NULL
 )
);

-- 3. The deferred completeness trigger must accept all three witness sources
-- and match each against the corresponding widget reference in the version
-- JSON. A composite widget is identified in the definition by
-- metric_ref.kind = 'composite'; text cards still produce no witness.
CREATE OR REPLACE FUNCTION dashboard_check_dependencies() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE expected integer; actual integer;
BEGIN
 SELECT count(*) INTO expected
 FROM dashboard_versions v
 CROSS JOIN LATERAL json_array_elements(v.definition->'widgets') w
 WHERE v.project_id=NEW.project_id AND v.dashboard_id=NEW.dashboard_id AND v.version=NEW.version
   AND COALESCE(json_typeof(w->'text_card')='object', false) = false;
 SELECT count(*) INTO actual FROM dashboard_dependencies WHERE project_id=NEW.project_id AND dashboard_id=NEW.dashboard_id AND version=NEW.version;
 IF EXISTS(SELECT 1 FROM dashboard_versions v WHERE v.project_id=NEW.project_id AND v.dashboard_id=NEW.dashboard_id AND v.version=NEW.version AND (
   v.dependencies->>'project_id' IS DISTINCT FROM v.project_id::text OR v.dependencies->>'environment' IS DISTINCT FROM v.environment OR jsonb_typeof(v.dependencies->'analyses') IS DISTINCT FROM 'array'
  )) THEN RAISE EXCEPTION 'dashboard dependency manifest scope is invalid' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM dashboard_versions v WHERE v.project_id=NEW.project_id AND v.dashboard_id=NEW.dashboard_id AND v.version=NEW.version AND jsonb_array_length(v.dependencies->'analyses')<>actual) OR EXISTS(
  SELECT 1 FROM dashboard_dependencies d JOIN dashboard_versions v USING(project_id,dashboard_id,version)
  WHERE d.project_id=NEW.project_id AND d.dashboard_id=NEW.dashboard_id AND d.version=NEW.version
  AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(v.dependencies->'analyses') a WHERE a->>'widget_id'=d.widget_id::text
    AND (
     (d.analysis_id IS NOT NULL AND a->>'analysis_id'=d.analysis_id::text AND a->>'analysis_version'=d.analysis_version::text)
     OR
     (d.metric_id IS NOT NULL AND a->>'metric_id'=d.metric_id::text AND a->>'metric_version'=d.metric_version::text)
     OR
     (d.formula_metric_id IS NOT NULL AND a->>'formula_metric_id'=d.formula_metric_id::text AND a->>'formula_metric_version'=d.formula_metric_version::text)
    )
    AND a->>'definition_hash'=d.definition_hash AND a->>'dependency_hash'=d.dependency_hash AND a->>'validation_hash'=d.validation_hash)
 ) THEN RAISE EXCEPTION 'dashboard dependency manifest does not match witnesses' USING ERRCODE='23514'; END IF;
 IF expected IS NULL OR expected<>actual OR EXISTS(
  SELECT 1 FROM dashboard_versions v CROSS JOIN LATERAL json_array_elements(v.definition->'widgets') w
  WHERE v.project_id=NEW.project_id AND v.dashboard_id=NEW.dashboard_id AND v.version=NEW.version
  AND COALESCE(json_typeof(w->'text_card')='object', false) = false
  AND NOT EXISTS(
   SELECT 1 FROM dashboard_dependencies d WHERE d.project_id=v.project_id AND d.dashboard_id=v.dashboard_id AND d.version=v.version AND d.widget_id=(w->>'widget_id')::uuid
    AND (
     (w->>'analysis_id' IS NOT NULL AND d.analysis_id=(w->>'analysis_id')::uuid AND d.analysis_version=(w->>'analysis_version')::bigint)
     OR
     (w->'metric_ref' IS NOT NULL AND COALESCE(w->'metric_ref'->>'kind','')<>'composite'
      AND d.metric_id=(w->'metric_ref'->>'metric_id')::uuid AND d.metric_version=(w->'metric_ref'->>'version')::bigint)
     OR
     (w->'metric_ref' IS NOT NULL AND w->'metric_ref'->>'kind'='composite'
      AND d.formula_metric_id=(w->'metric_ref'->>'metric_id')::uuid AND d.formula_metric_version=(w->'metric_ref'->>'version')::bigint)
    )
  )
 ) THEN
  RAISE EXCEPTION 'dashboard publication dependencies are incomplete' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END;
$$;
