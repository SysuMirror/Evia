-- Dashboard widgets may pin an immutable metric version directly instead of
-- going through a saved-analysis version. The witness tables keep their generic
-- identity columns and gain nullable dual-target foreign keys, exactly one of
-- which must be populated. Existing analysis-only rows are untouched because
-- composite foreign keys use MATCH SIMPLE and skip any row containing a NULL.

-- The shape validator is referenced by CHECK constraints, so drop those
-- constraints before replacing the immutable function, then re-add them.
ALTER TABLE dashboards DROP CONSTRAINT dashboards_definition_shape;
ALTER TABLE dashboard_versions DROP CONSTRAINT dashboard_versions_definition_shape;

CREATE OR REPLACE FUNCTION dashboard_definition_valid(data json) RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE w json; other json; n integer; seen text[] := '{}'; orders integer[] := '{}'; x integer; y integer; width integer; height integer; reading integer;
 has_analysis boolean; has_metric boolean;
BEGIN
 IF json_typeof(data)<>'object' OR NOT (data::jsonb ?& ARRAY['title','description','widgets']) OR json_typeof(data->'title')<>'string' OR length(data->>'title') NOT BETWEEN 1 AND 128 OR data->>'title'<>btrim(data->>'title') OR json_typeof(data->'description')<>'string' OR length(data->>'description')>2000 OR json_typeof(data->'widgets')<>'array' THEN RETURN false; END IF;
 n:=json_array_length(data->'widgets');
 IF n NOT BETWEEN 1 AND 12 THEN RETURN false; END IF;
 FOR w IN SELECT value FROM json_array_elements(data->'widgets') LOOP
  IF json_typeof(w)<>'object' OR NOT(w::jsonb ?& ARRAY['widget_id','title','reading_order','layout']) OR COALESCE(w->>'widget_id','')!~'^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$' OR json_typeof(w->'title')<>'string' OR length(w->>'title') NOT BETWEEN 1 AND 128 OR w->>'title'<>btrim(w->>'title') OR json_typeof(w->'reading_order')<>'number' OR json_typeof(w->'layout')<>'object' OR NOT((w->'layout')::jsonb ?& ARRAY['x','y','width','height']) OR json_typeof(w->'layout'->'x')<>'number' OR json_typeof(w->'layout'->'y')<>'number' OR json_typeof(w->'layout'->'width')<>'number' OR json_typeof(w->'layout'->'height')<>'number' THEN RETURN false; END IF;
  has_analysis := (COALESCE(w->>'analysis_id','') ~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$') AND json_typeof(w->'analysis_version')='number' AND (w->>'analysis_version')::bigint BETWEEN 1 AND 9007199254740991;
  has_metric := (COALESCE(w->'metric_ref'->>'metric_id','') ~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$') AND json_typeof(w->'metric_ref'->'version')='number' AND (w->'metric_ref'->>'version')::bigint BETWEEN 1 AND 9007199254740991;
  IF has_analysis = has_metric THEN RETURN false; END IF;
  x:=(w->'layout'->>'x')::integer; y:=(w->'layout'->>'y')::integer; width:=(w->'layout'->>'width')::integer; height:=(w->'layout'->>'height')::integer; reading:=(w->>'reading_order')::integer;
  IF x IS NULL OR y IS NULL OR width IS NULL OR height IS NULL OR reading IS NULL OR x<0 OR y<0 OR width NOT BETWEEN 1 AND 12 OR height NOT BETWEEN 1 AND 24 OR x+width>12 OR y+height>1000 OR reading NOT BETWEEN 0 AND n-1 OR (w->>'widget_id')=ANY(seen) OR reading=ANY(orders) THEN RETURN false; END IF;
  FOR other IN SELECT value FROM json_array_elements(data->'widgets') LOOP
   IF (other->>'widget_id')=ANY(seen) AND x<(other->'layout'->>'x')::integer+(other->'layout'->>'width')::integer AND (other->'layout'->>'x')::integer<x+width AND y<(other->'layout'->>'y')::integer+(other->'layout'->>'height')::integer AND (other->'layout'->>'y')::integer<y+height THEN RETURN false; END IF;
  END LOOP;
  seen:=array_append(seen,w->>'widget_id'); orders:=array_append(orders,reading);
 END LOOP;
 RETURN true;
EXCEPTION WHEN OTHERS THEN RETURN false;
END;
$$;

ALTER TABLE dashboards ADD CONSTRAINT dashboards_definition_shape CHECK(dashboard_definition_valid(definition));
ALTER TABLE dashboard_versions ADD CONSTRAINT dashboard_versions_definition_shape CHECK(dashboard_definition_valid(definition));

-- Dependency witnesses: relax the analysis target and add a metric target.
ALTER TABLE dashboard_dependencies
 ALTER COLUMN analysis_id DROP NOT NULL,
 ALTER COLUMN analysis_version DROP NOT NULL,
 ADD COLUMN metric_id uuid,
 ADD COLUMN metric_version bigint,
 ADD CONSTRAINT dashboard_dependencies_metric_ref_fkey
  FOREIGN KEY(project_id,environment,metric_id,metric_version)
  REFERENCES metric_versions(project_id,environment,metric_id,version),
 ADD CONSTRAINT dashboard_dependencies_metric_unique
  UNIQUE(project_id,environment,dashboard_id,version,widget_id,metric_id,metric_version),
 ADD CONSTRAINT dashboard_dependencies_source_sound CHECK(
  (
   analysis_id IS NOT NULL AND analysis_version IS NOT NULL
   AND metric_id IS NULL AND metric_version IS NULL
  ) OR (
   metric_id IS NOT NULL AND metric_version IS NOT NULL
   AND analysis_id IS NULL AND analysis_version IS NULL
  )
 );

-- Run widgets mirror the same dual-target shape. The existing analysis foreign
-- key still applies to analysis rows and is skipped for metric rows (NULLs).
ALTER TABLE dashboard_run_widgets
 ALTER COLUMN analysis_id DROP NOT NULL,
 ALTER COLUMN analysis_version DROP NOT NULL,
 ADD COLUMN metric_id uuid,
 ADD COLUMN metric_version bigint,
 ADD CONSTRAINT dashboard_run_widgets_metric_ref_fkey
  FOREIGN KEY(project_id,environment,dashboard_id,version,widget_id,metric_id,metric_version)
  REFERENCES dashboard_dependencies(project_id,environment,dashboard_id,version,widget_id,metric_id,metric_version),
 ADD CONSTRAINT dashboard_run_widgets_source_sound CHECK(
  (
   analysis_id IS NOT NULL AND analysis_version IS NOT NULL
   AND metric_id IS NULL AND metric_version IS NULL
  ) OR (
   metric_id IS NOT NULL AND metric_version IS NOT NULL
   AND analysis_id IS NULL AND analysis_version IS NULL
  )
 );

-- The deferred completeness trigger must accept either witness source and
-- match it against the corresponding widget reference in the version JSON.
CREATE OR REPLACE FUNCTION dashboard_check_dependencies() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE expected integer; actual integer;
BEGIN
 SELECT json_array_length(definition->'widgets') INTO expected FROM dashboard_versions WHERE project_id=NEW.project_id AND dashboard_id=NEW.dashboard_id AND version=NEW.version;
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
    )
    AND a->>'definition_hash'=d.definition_hash AND a->>'dependency_hash'=d.dependency_hash AND a->>'validation_hash'=d.validation_hash)
 ) THEN RAISE EXCEPTION 'dashboard dependency manifest does not match witnesses' USING ERRCODE='23514'; END IF;
 IF expected IS NULL OR expected<>actual OR EXISTS(
  SELECT 1 FROM dashboard_versions v CROSS JOIN LATERAL json_array_elements(v.definition->'widgets') w
  WHERE v.project_id=NEW.project_id AND v.dashboard_id=NEW.dashboard_id AND v.version=NEW.version
  AND NOT EXISTS(
   SELECT 1 FROM dashboard_dependencies d WHERE d.project_id=v.project_id AND d.dashboard_id=v.dashboard_id AND d.version=v.version AND d.widget_id=(w->>'widget_id')::uuid
    AND (
     (w->>'analysis_id' IS NOT NULL AND d.analysis_id=(w->>'analysis_id')::uuid AND d.analysis_version=(w->>'analysis_version')::bigint)
     OR
     (w->'metric_ref' IS NOT NULL AND d.metric_id=(w->'metric_ref'->>'metric_id')::uuid AND d.metric_version=(w->'metric_ref'->>'version')::bigint)
    )
  )
 ) THEN
  RAISE EXCEPTION 'dashboard publication dependencies are incomplete' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END;
$$;
