-- Mutable dashboard drafts pin immutable saved-analysis versions. Publication
-- witnesses enforce the project, environment and all three semantic hashes.
CREATE TABLE dashboards (
 project_id uuid NOT NULL REFERENCES projects(id),
 dashboard_id uuid NOT NULL,
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 title text NOT NULL CHECK(length(title) BETWEEN 1 AND 128 AND title=btrim(title)),
 draft_revision bigint NOT NULL DEFAULT 1 CHECK(draft_revision BETWEEN 1 AND 9007199254740991),
 published_version bigint CHECK(published_version BETWEEN 1 AND 9007199254740991),
 definition json NOT NULL CHECK(json_typeof(definition)='object' AND octet_length(definition::text)<=65536),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(project_id,dashboard_id),
 UNIQUE(project_id,environment,dashboard_id),
 CHECK(definition::jsonb ? 'title' AND json_typeof(definition->'title')='string' AND title=definition->>'title'),
 CHECK(definition::jsonb ? 'description' AND json_typeof(definition->'description')='string' AND length(definition->>'description')<=2000),
 CHECK(json_typeof(definition->'widgets')='array' AND json_array_length(definition->'widgets') BETWEEN 1 AND 12)
);
CREATE INDEX dashboards_scope ON dashboards(project_id,environment,dashboard_id);
CREATE TABLE dashboard_versions (
 project_id uuid NOT NULL,
 dashboard_id uuid NOT NULL,
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 version bigint NOT NULL CHECK(version BETWEEN 1 AND 9007199254740991),
 draft_revision bigint NOT NULL CHECK(draft_revision BETWEEN 1 AND 9007199254740991),
 definition json NOT NULL CHECK(json_typeof(definition)='object' AND octet_length(definition::text)<=65536),
 dependencies jsonb NOT NULL CHECK(jsonb_typeof(dependencies)='object' AND octet_length(dependencies::text)<=32768),
 definition_hash text NOT NULL CHECK(definition_hash ~ '^[a-f0-9]{64}$'),
 dependency_hash text NOT NULL CHECK(dependency_hash ~ '^[a-f0-9]{64}$'),
 validation_hash text NOT NULL CHECK(validation_hash ~ '^[a-f0-9]{64}$'),
 created_at timestamptz NOT NULL DEFAULT now(),
 created_by uuid NOT NULL REFERENCES accounts(id),
 PRIMARY KEY(project_id,dashboard_id,version),
 UNIQUE(project_id,environment,dashboard_id,version),
 FOREIGN KEY(project_id,environment,dashboard_id) REFERENCES dashboards(project_id,environment,dashboard_id)
);
ALTER TABLE dashboards ADD CONSTRAINT dashboards_published_pointer
 FOREIGN KEY(project_id,environment,dashboard_id,published_version)
 REFERENCES dashboard_versions(project_id,environment,dashboard_id,version) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE saved_analysis_versions ADD CONSTRAINT saved_analysis_versions_hash_witness
 UNIQUE(project_id,environment,analysis_id,version,definition_hash,dependency_hash,validation_hash);
CREATE TABLE dashboard_dependencies (
 project_id uuid NOT NULL,
 environment text NOT NULL,
 dashboard_id uuid NOT NULL,
 version bigint NOT NULL,
 widget_id uuid NOT NULL,
 analysis_id uuid NOT NULL,
 analysis_version bigint NOT NULL,
 definition_hash text NOT NULL,
 dependency_hash text NOT NULL,
 validation_hash text NOT NULL,
 PRIMARY KEY(project_id,dashboard_id,version,widget_id),
 UNIQUE(project_id,environment,dashboard_id,version,widget_id,analysis_id,analysis_version),
 FOREIGN KEY(project_id,environment,dashboard_id,version) REFERENCES dashboard_versions(project_id,environment,dashboard_id,version),
 FOREIGN KEY(project_id,environment,analysis_id,analysis_version,definition_hash,dependency_hash,validation_hash)
 REFERENCES saved_analysis_versions(project_id,environment,analysis_id,version,definition_hash,dependency_hash,validation_hash)
);
CREATE TABLE dashboard_idempotency (
 actor_id uuid NOT NULL REFERENCES accounts(id),
 project_id uuid NOT NULL REFERENCES projects(id),
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 operation text NOT NULL,
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 1 AND 200),
 request_hash text NOT NULL CHECK(request_hash ~ '^[a-f0-9]{64}$'),
 response jsonb NOT NULL CHECK(jsonb_typeof(response)='object' AND octet_length(response::text)<=196608),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(actor_id,project_id,environment,operation,idempotency_key)
);
CREATE TABLE dashboard_runs (
 project_id uuid NOT NULL,
 environment text NOT NULL,
 dashboard_id uuid NOT NULL,
 version bigint NOT NULL,
 run_id uuid NOT NULL,
 created_by uuid NOT NULL REFERENCES accounts(id),
 time_from timestamptz NOT NULL,
 time_to timestamptz NOT NULL CHECK(time_to>=time_from),
 timezone text NOT NULL,
 widget_filters json NOT NULL CHECK(json_typeof(widget_filters)='array' AND json_array_length(widget_filters)<=12 AND octet_length(widget_filters::text)<=65536),
 -- Timestamp columns are microsecond projections for relational inspection.
 -- Exact normalized runtime bounds, including nanoseconds, live in this JSON.
 execution_input json NOT NULL CHECK(octet_length(execution_input::text)<=65536),
 CHECK((json_typeof(execution_input)='object'
  AND json_typeof(execution_input->'time_range')='object'
  AND json_typeof(execution_input->'time_range'->'from')='string'
  AND json_typeof(execution_input->'time_range'->'to')='string'
  AND json_typeof(execution_input->'timezone')='string'
  AND execution_input->>'timezone'=timezone
  AND COALESCE(execution_input::jsonb->'widget_filters','[]'::jsonb)=widget_filters::jsonb) IS TRUE),
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL,
 PRIMARY KEY(project_id,run_id),
 UNIQUE(project_id,environment,dashboard_id,version,run_id),
 FOREIGN KEY(project_id,environment,dashboard_id,version) REFERENCES dashboard_versions(project_id,environment,dashboard_id,version),
 CHECK(expires_at>created_at)
);
CREATE TABLE dashboard_run_widgets (
 project_id uuid NOT NULL,
 environment text NOT NULL,
 dashboard_id uuid NOT NULL,
 version bigint NOT NULL,
 run_id uuid NOT NULL,
 widget_id uuid NOT NULL,
 analysis_id uuid NOT NULL,
 analysis_version bigint NOT NULL,
 position integer NOT NULL CHECK(position BETWEEN 0 AND 11),
 query_id uuid,
 original_query_id uuid NOT NULL,
 query_created_at timestamptz NOT NULL,
 PRIMARY KEY(project_id,run_id,widget_id),
 UNIQUE(project_id,run_id,position),
 FOREIGN KEY(project_id,environment,dashboard_id,version,run_id) REFERENCES dashboard_runs(project_id,environment,dashboard_id,version,run_id),
 FOREIGN KEY(project_id,environment,dashboard_id,version,widget_id,analysis_id,analysis_version)
 REFERENCES dashboard_dependencies(project_id,environment,dashboard_id,version,widget_id,analysis_id,analysis_version),
 FOREIGN KEY(project_id,environment,query_id) REFERENCES analysis_query_jobs(project_id,environment,id) ON DELETE SET NULL(query_id),
 CHECK(query_id IS NULL OR query_id=original_query_id)
);
CREATE INDEX dashboard_run_widgets_query ON dashboard_run_widgets(project_id,environment,query_id) WHERE query_id IS NOT NULL;
CREATE FUNCTION dashboard_preserve_scope() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.dashboard_id IS DISTINCT FROM OLD.dashboard_id OR NEW.environment IS DISTINCT FROM OLD.environment OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
  RAISE EXCEPTION 'dashboard scope is immutable' USING ERRCODE='23514';
 END IF;
 IF NEW.published_version IS DISTINCT FROM OLD.published_version AND (NEW.published_version IS NULL OR NEW.published_version<>COALESCE(OLD.published_version,0)+1) THEN
  RAISE EXCEPTION 'dashboard published pointer must advance one version' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER dashboards_scope_immutable BEFORE UPDATE ON dashboards FOR EACH ROW EXECUTE FUNCTION dashboard_preserve_scope();
CREATE TRIGGER dashboard_versions_immutable BEFORE UPDATE OR DELETE ON dashboard_versions FOR EACH ROW EXECUTE FUNCTION catalog_reject_immutable_change();
CREATE TRIGGER dashboard_dependencies_immutable BEFORE UPDATE OR DELETE ON dashboard_dependencies FOR EACH ROW EXECUTE FUNCTION catalog_reject_immutable_change();
CREATE TRIGGER dashboard_idempotency_immutable BEFORE UPDATE OR DELETE ON dashboard_idempotency FOR EACH ROW EXECUTE FUNCTION catalog_reject_immutable_change();
CREATE TRIGGER dashboard_runs_immutable BEFORE UPDATE OR DELETE ON dashboard_runs FOR EACH ROW EXECUTE FUNCTION catalog_reject_immutable_change();
CREATE FUNCTION dashboard_preserve_run_widget() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' THEN
  IF OLD.query_id IS NOT NULL AND NEW.query_id IS NULL AND (to_jsonb(NEW)-'query_id')=(to_jsonb(OLD)-'query_id') AND NOT EXISTS(SELECT 1 FROM analysis_query_jobs WHERE id=OLD.query_id) THEN
   RETURN NEW;
  END IF;
 END IF;
 RAISE EXCEPTION 'dashboard run widget is immutable' USING ERRCODE='23514';
END;
$$;
CREATE TRIGGER dashboard_run_widgets_immutable BEFORE UPDATE OR DELETE ON dashboard_run_widgets FOR EACH ROW EXECUTE FUNCTION dashboard_preserve_run_widget();
-- A deferred check prevents publication without its complete relational witness.
CREATE FUNCTION dashboard_check_dependencies() RETURNS trigger LANGUAGE plpgsql AS $$
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
  AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(v.dependencies->'analyses') a WHERE a->>'widget_id'=d.widget_id::text AND a->>'analysis_id'=d.analysis_id::text AND a->>'analysis_version'=d.analysis_version::text AND a->>'definition_hash'=d.definition_hash AND a->>'dependency_hash'=d.dependency_hash AND a->>'validation_hash'=d.validation_hash)
 ) THEN RAISE EXCEPTION 'dashboard dependency manifest does not match witnesses' USING ERRCODE='23514'; END IF;
 IF expected IS NULL OR expected<>actual OR EXISTS(
  SELECT 1 FROM dashboard_versions v CROSS JOIN LATERAL json_array_elements(v.definition->'widgets') w
  WHERE v.project_id=NEW.project_id AND v.dashboard_id=NEW.dashboard_id AND v.version=NEW.version
  AND NOT EXISTS(SELECT 1 FROM dashboard_dependencies d WHERE d.project_id=v.project_id AND d.dashboard_id=v.dashboard_id AND d.version=v.version AND d.widget_id=(w->>'widget_id')::uuid AND d.analysis_id=(w->>'analysis_id')::uuid AND d.analysis_version=(w->>'analysis_version')::bigint)
 ) THEN
  RAISE EXCEPTION 'dashboard publication dependencies are incomplete' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER dashboard_versions_dependencies AFTER INSERT ON dashboard_versions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION dashboard_check_dependencies();
CREATE CONSTRAINT TRIGGER dashboard_dependencies_complete AFTER INSERT ON dashboard_dependencies DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION dashboard_check_dependencies();
-- SQL callers receive the same core persisted shape bounds as the adapter.
CREATE FUNCTION dashboard_definition_valid(data json) RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE w json; other json; n integer; seen text[] := '{}'; orders integer[] := '{}'; x integer; y integer; width integer; height integer; reading integer;
BEGIN
 IF json_typeof(data)<>'object' OR NOT (data::jsonb ?& ARRAY['title','description','widgets']) OR json_typeof(data->'title')<>'string' OR length(data->>'title') NOT BETWEEN 1 AND 128 OR data->>'title'<>btrim(data->>'title') OR json_typeof(data->'description')<>'string' OR length(data->>'description')>2000 OR json_typeof(data->'widgets')<>'array' THEN RETURN false; END IF;
 n:=json_array_length(data->'widgets');
 IF n NOT BETWEEN 1 AND 12 THEN RETURN false; END IF;
 FOR w IN SELECT value FROM json_array_elements(data->'widgets') LOOP
  IF json_typeof(w)<>'object' OR NOT(w::jsonb ?& ARRAY['widget_id','analysis_id','analysis_version','title','reading_order','layout']) OR COALESCE(w->>'widget_id','')!~'^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$' OR COALESCE(w->>'analysis_id','')!~'^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$' OR json_typeof(w->'analysis_version')<>'number' OR (w->>'analysis_version')::bigint NOT BETWEEN 1 AND 9007199254740991 OR json_typeof(w->'title')<>'string' OR length(w->>'title') NOT BETWEEN 1 AND 128 OR w->>'title'<>btrim(w->>'title') OR json_typeof(w->'reading_order')<>'number' OR json_typeof(w->'layout')<>'object' OR NOT((w->'layout')::jsonb ?& ARRAY['x','y','width','height']) OR json_typeof(w->'layout'->'x')<>'number' OR json_typeof(w->'layout'->'y')<>'number' OR json_typeof(w->'layout'->'width')<>'number' OR json_typeof(w->'layout'->'height')<>'number' THEN RETURN false; END IF;
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
