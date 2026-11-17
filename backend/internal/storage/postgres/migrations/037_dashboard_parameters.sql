-- Dashboards gain reusable runtime parameters declared once at the dashboard
-- level and bound per widget to that widget's own pinned event fields. The
-- shape validator is the database defense-in-depth gate; the Go compiler
-- remains the authoritative semantic checker (required values, type bounds,
-- duplicate fields) when a run is admitted.
--
-- The validator is referenced by CHECK constraints, so drop those constraints
-- before replacing the immutable function, then re-add them.
ALTER TABLE dashboards DROP CONSTRAINT dashboards_definition_shape;
ALTER TABLE dashboard_versions DROP CONSTRAINT dashboard_versions_definition_shape;

CREATE OR REPLACE FUNCTION dashboard_definition_valid(data json) RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE w json; other json; n integer; seen text[] := '{}'; orders integer[] := '{}'; x integer; y integer; width integer; height integer; reading integer;
 has_analysis boolean; has_metric boolean;
 p json; declared text[] := '{}';
 b json; bound_params text[]; bound_fields text[]; field_key text;
BEGIN
 IF json_typeof(data)<>'object' OR NOT (data::jsonb ?& ARRAY['title','description','widgets']) OR json_typeof(data->'title')<>'string' OR length(data->>'title') NOT BETWEEN 1 AND 128 OR data->>'title'<>btrim(data->>'title') OR json_typeof(data->'description')<>'string' OR length(data->>'description')>2000 OR json_typeof(data->'widgets')<>'array' THEN RETURN false; END IF;
 IF data::jsonb ? 'parameters' THEN
  IF json_typeof(data->'parameters')<>'array' OR json_array_length(data->'parameters') NOT BETWEEN 0 AND 12 THEN RETURN false; END IF;
  FOR p IN SELECT value FROM json_array_elements(data->'parameters') LOOP
   IF json_typeof(p)<>'object' OR NOT(p::jsonb ?& ARRAY['parameter_id','label','type','operator','required']) OR json_typeof(p->'parameter_id')<>'string' OR length(p->>'parameter_id') NOT BETWEEN 1 AND 64 OR json_typeof(p->'label')<>'string' OR length(p->>'label') NOT BETWEEN 1 AND 128 OR json_typeof(p->'required')<>'boolean' OR (p->>'parameter_id')=ANY(declared) THEN RETURN false; END IF;
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
  has_analysis := (COALESCE(w->>'analysis_id','') ~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$') AND json_typeof(w->'analysis_version')='number' AND (w->>'analysis_version')::bigint BETWEEN 1 AND 9007199254740991;
  has_metric := (COALESCE(w->'metric_ref'->>'metric_id','') ~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$') AND json_typeof(w->'metric_ref'->'version')='number' AND (w->'metric_ref'->>'version')::bigint BETWEEN 1 AND 9007199254740991;
  IF has_analysis = has_metric THEN RETURN false; END IF;
  IF w::jsonb ? 'parameter_bindings' THEN
   IF json_typeof(w->'parameter_bindings')<>'array' OR json_array_length(w->'parameter_bindings') NOT BETWEEN 0 AND 12 THEN RETURN false; END IF;
   bound_params:='{}'; bound_fields:='{}';
   FOR b IN SELECT value FROM json_array_elements(w->'parameter_bindings') LOOP
    IF json_typeof(b)<>'object' OR NOT(b::jsonb ?& ARRAY['parameter_id','field']) OR json_typeof(b->'parameter_id')<>'string' OR NOT ((b->>'parameter_id')=ANY(declared)) OR (b->>'parameter_id')=ANY(bound_params) THEN RETURN false; END IF;
    IF json_typeof(b->'field')<>'object' OR NOT((b->'field')::jsonb ?& ARRAY['kind','key']) OR json_typeof(b->'field'->'key')<>'string' OR length(b->'field'->>'key')=0 THEN RETURN false; END IF;
    IF b->'field'->>'kind'='system' THEN
     IF (b->'field')::jsonb ? 'event_key' OR (b->'field')::jsonb ? 'schema_version' THEN RETURN false; END IF;
    ELSIF b->'field'->>'kind'='property' THEN
     IF COALESCE(json_typeof(b->'field'->'event_key'),'null')<>'string' OR length(b->'field'->>'event_key')=0 OR COALESCE(json_typeof(b->'field'->'schema_version'),'null')<>'number' OR (b->'field'->>'schema_version')::bigint NOT BETWEEN 1 AND 9007199254740991 THEN RETURN false; END IF;
    ELSE
     RETURN false;
    END IF;
    field_key:=(b->'field'->>'kind')||':'||(b->'field'->>'key')||':'||COALESCE(b->'field'->>'event_key','')||':'||COALESCE(b->'field'->>'schema_version','');
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
