-- Mutation 038: dashboard text-card widgets.
--
-- A widget may now carry a presentation-only text_card object instead of an
-- executable source. The shape validator is replaced to enforce the three-way
-- exclusive choice (analysis XOR metric XOR text_card), bound the Markdown
-- payload and reject raw HTML/images/blocked URL schemes at the database
-- boundary. Deep Markdown link validation stays in the Go normalizer; this
-- function provides structural defense in depth. Existing definitions without
-- text_card keep validating unchanged.
ALTER TABLE dashboards DROP CONSTRAINT IF EXISTS dashboards_definition_shape;
ALTER TABLE dashboard_versions DROP CONSTRAINT IF EXISTS dashboard_versions_definition_shape;

CREATE OR REPLACE FUNCTION dashboard_definition_valid(data json) RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
 p json;
 w json;
 other json;
 b json;
 n integer;
 i integer;
 has_analysis boolean;
 has_metric boolean;
 has_text boolean;
 declared text[] := '{}';
 bound_params text[];
 bound_fields text[];
 field_key text;
 text_content text;
 x integer;
 y integer;
 width integer;
 height integer;
 reading integer;
 seen text[] := '{}';
 orders integer[] := '{}';
BEGIN
 IF json_typeof(data)<>'object' OR NOT (data::jsonb ?& ARRAY['title','description','widgets']) OR json_typeof(data->'title')<>'string' OR json_typeof(data->'description')<>'string' OR json_typeof(data->'widgets')<>'array' THEN RETURN false; END IF;
 IF length(data->>'title') NOT BETWEEN 1 AND 128 OR data->>'title'<>btrim(data->>'title') OR length(data->>'description')>2000 THEN RETURN false; END IF;
 IF data::jsonb ? 'parameters' THEN
  IF json_typeof(data->'parameters')<>'array' OR json_array_length(data->'parameters') NOT BETWEEN 0 AND 12 THEN RETURN false; END IF;
  FOR p IN SELECT value FROM json_array_elements(data->'parameters') LOOP
   IF json_typeof(p)<>'object' OR NOT(p::jsonb ?& ARRAY['parameter_id','label','type','operator','required']) OR COALESCE(length(p->>'parameter_id'),0) NOT BETWEEN 1 AND 64 OR p->>'parameter_id'=ANY(declared) OR json_typeof(p->'label')<>'string' OR length(p->>'label') NOT BETWEEN 1 AND 128 THEN RETURN false; END IF;
   IF p->>'type'='string' THEN
    IF p->>'operator' NOT IN ('eq','neq','in','prefix','contains') THEN RETURN false; END IF;
   ELSIF p->>'type'='number' THEN
    IF p->>'operator' NOT IN ('eq','neq','in','gt','gte','lt','lte') THEN RETURN false; END IF;
   ELSIF p->>'type'='boolean' THEN
    IF p->>'operator' NOT IN ('eq','neq') THEN RETURN false; END IF;
   ELSE
    RETURN false;
   END IF;
   IF (p::jsonb ? 'default') AND json_typeof(p->'default') NOT IN ('null','string','number','boolean','array') THEN RETURN false; END IF;
   declared:=array_append(declared,p->>'parameter_id');
  END LOOP;
 END IF;
 n:=json_array_length(data->'widgets');
 IF n NOT BETWEEN 1 AND 12 THEN RETURN false; END IF;
 FOR w IN SELECT value FROM json_array_elements(data->'widgets') LOOP
  IF json_typeof(w)<>'object' OR NOT(w::jsonb ?& ARRAY['widget_id','title','reading_order','layout']) OR COALESCE(w->>'widget_id','')!~'^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$' OR json_typeof(w->'title')<>'string' OR length(w->>'title') NOT BETWEEN 1 AND 128 OR w->>'title'<>btrim(w->>'title') OR json_typeof(w->'reading_order')<>'number' OR json_typeof(w->'layout')<>'object' OR NOT((w->'layout')::jsonb ?& ARRAY['x','y','width','height']) OR json_typeof(w->'layout'->'x')<>'number' OR json_typeof(w->'layout'->'y')<>'number' OR json_typeof(w->'layout'->'width')<>'number' OR json_typeof(w->'layout'->'height')<>'number' THEN RETURN false; END IF;
  has_analysis := COALESCE((COALESCE(w->>'analysis_id','') ~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$') AND json_typeof(w->'analysis_version')='number' AND (w->>'analysis_version')::bigint BETWEEN 1 AND 9007199254740991, false);
  has_metric := COALESCE((COALESCE(w->'metric_ref'->>'metric_id','') ~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$') AND json_typeof(w->'metric_ref'->'version')='number' AND (w->'metric_ref'->>'version')::bigint BETWEEN 1 AND 9007199254740991, false);
  has_text := COALESCE(json_typeof(w->'text_card')='object', false);
  -- Exactly one source kind across the three exclusive choices.
  IF (has_analysis AND has_metric) OR (has_analysis AND has_text) OR (has_metric AND has_text) OR (NOT has_analysis AND NOT has_metric AND NOT has_text) THEN RETURN false; END IF;
  IF has_text THEN
   IF json_typeof(w->'text_card'->'content')<>'string' THEN RETURN false; END IF;
   text_content := w->'text_card'->>'content';
   IF length(text_content) NOT BETWEEN 1 AND 4096 OR text_content ~* '</?[a-z!]' OR text_content ~* '!\[' OR text_content ~* '(javascript|vbscript|data)\s*:' THEN RETURN false; END IF;
   -- Fenced code blocks must be closed: an even delimiter count yields odd array cardinality.
   IF cardinality(string_to_array(text_content,'```')) % 2 = 0 THEN RETURN false; END IF;
   -- Presentation cards cannot bind runtime parameters.
   IF (w::jsonb ? 'parameter_bindings') AND json_array_length(w->'parameter_bindings') > 0 THEN RETURN false; END IF;
  END IF;
  IF NOT has_text AND w::jsonb ? 'parameter_bindings' THEN
   IF json_typeof(w->'parameter_bindings')<>'array' OR json_array_length(w->'parameter_bindings') NOT BETWEEN 0 AND 12 THEN RETURN false; END IF;
   bound_params:='{}'; bound_fields:='{}';
   FOR b IN SELECT value FROM json_array_elements(w->'parameter_bindings') LOOP
    IF json_typeof(b)<>'object' OR NOT(b::jsonb ?& ARRAY['parameter_id','field']) OR json_typeof(b->'parameter_id')<>'string' OR NOT ((b->>'parameter_id')=ANY(declared)) THEN RETURN false; END IF;
    IF (b->>'parameter_id')=ANY(bound_params) THEN RETURN false; END IF;
    IF json_typeof(b->'field')<>'object' OR NOT((b->'field')::jsonb ?& ARRAY['kind','key']) THEN RETURN false; END IF;
    IF b->'field'->>'kind'='system' THEN
     IF (COALESCE(b->'field'->>'event_key','')<>'') OR (COALESCE(b->'field'->>'schema_version','')<>'') THEN RETURN false; END IF;
     field_key:=(b->'field'->>'kind')||':'||(b->'field'->>'key');
    ELSIF b->'field'->>'kind'='property' THEN
     IF COALESCE(json_typeof(b->'field'->'event_key'),'null')<>'string' OR length(b->'field'->>'event_key')=0 OR COALESCE(json_typeof(b->'field'->'schema_version'),'null')<>'number' OR (b->'field'->>'schema_version')::bigint NOT BETWEEN 1 AND 9007199254740991 THEN RETURN false; END IF;
     field_key:=(b->'field'->>'kind')||':'||(b->'field'->>'key')||':'||COALESCE(b->'field'->>'event_key','')||':'||COALESCE(b->'field'->>'schema_version','');
    ELSE
     RETURN false;
    END IF;
    IF field_key=ANY(bound_fields) THEN RETURN false; END IF;
    bound_params:=array_append(bound_params,b->>'parameter_id');
    bound_fields:=array_append(bound_fields,field_key);
   END LOOP;
  END IF;
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

-- The deferred dependency trigger must count only executable widgets:
-- presentation-only text cards have no immutable source and no witness row.
-- This is the migration 034 dual-target function with text-card filtering.
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
     (w->'metric_ref' IS NOT NULL AND d.metric_id=(w->'metric_ref'->>'metric_id')::uuid AND d.metric_version=(w->'metric_ref'->>'version')::bigint)
    )
  )
 ) THEN
  RAISE EXCEPTION 'dashboard publication dependencies are incomplete' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END;
$$;
