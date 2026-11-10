-- Composite (formula) metrics combine pinned immutable atomic metric versions
-- through a restricted typed expression. They are an isolated aggregate so the
-- atomic metric hot path, its tables and CHECK shape remain untouched.
-- Definition JSON keeps normalized wire bytes; JSONB would re-render spacing.
CREATE TABLE formula_metrics (
 project_id uuid NOT NULL REFERENCES projects(id),
 metric_id uuid NOT NULL,
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 title text NOT NULL CHECK(length(title) BETWEEN 1 AND 128 AND title=btrim(title)),
 draft_revision bigint NOT NULL DEFAULT 1 CHECK(draft_revision BETWEEN 1 AND 9007199254740991),
 published_version bigint CHECK(published_version BETWEEN 1 AND 9007199254740991),
 definition json NOT NULL CHECK(json_typeof(definition)='object' AND octet_length(definition::text)<=131072),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(project_id,metric_id),
 UNIQUE(project_id,environment,metric_id),
 CHECK(definition::jsonb ? 'title' AND json_typeof(definition->'title')='string' AND title=definition->>'title'
   AND definition::jsonb ? 'description' AND json_typeof(definition->'description')='string' AND length(definition->>'description')<=2000
   AND definition::jsonb ? 'formula' AND json_typeof(definition->'formula')='object'
   AND json_typeof(definition->'formula'->'expression')='object'
   AND json_typeof(definition->'formula'->'unit')='string'
   AND NOT definition::jsonb ? 'event_versions' AND NOT definition::jsonb ? 'aggregation')
);
CREATE INDEX formula_metrics_scope ON formula_metrics(project_id,environment,metric_id);

CREATE TABLE formula_metric_versions (
 project_id uuid NOT NULL,
 metric_id uuid NOT NULL,
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 version bigint NOT NULL CHECK(version BETWEEN 1 AND 9007199254740991),
 draft_revision bigint NOT NULL CHECK(draft_revision BETWEEN 1 AND 9007199254740991),
 definition json NOT NULL CHECK(json_typeof(definition)='object' AND octet_length(definition::text)<=131072),
 dependencies jsonb NOT NULL CHECK(jsonb_typeof(dependencies)='object' AND octet_length(dependencies::text)<=32768),
 definition_hash text NOT NULL CHECK(definition_hash ~ '^[a-f0-9]{64}$'),
 dependency_hash text NOT NULL CHECK(dependency_hash ~ '^[a-f0-9]{64}$'),
 validation_hash text NOT NULL CHECK(validation_hash ~ '^[a-f0-9]{64}$'),
 created_at timestamptz NOT NULL DEFAULT now(),
 created_by uuid NOT NULL REFERENCES principals(id),
 PRIMARY KEY(project_id,metric_id,version),
 UNIQUE(project_id,environment,metric_id,version),
 FOREIGN KEY(project_id,environment,metric_id) REFERENCES formula_metrics(project_id,environment,metric_id)
);
ALTER TABLE formula_metrics ADD CONSTRAINT formula_metrics_published_pointer
 FOREIGN KEY(project_id,environment,metric_id,published_version)
 REFERENCES formula_metric_versions(project_id,environment,metric_id,version) DEFERRABLE INITIALLY DEFERRED;

-- Each witness pins one referenced immutable atomic metric version. The foreign
-- key to metric_versions enforces publication and the same project/environment
-- at the storage boundary, so a formula cannot pin a draft or cross-scope metric.
CREATE TABLE formula_metric_dependencies (
 project_id uuid NOT NULL,
 environment text NOT NULL,
 metric_id uuid NOT NULL,
 version bigint NOT NULL,
 ref_metric_id uuid NOT NULL,
 ref_version bigint NOT NULL,
 definition_hash text NOT NULL CHECK(definition_hash ~ '^[a-f0-9]{64}$'),
 PRIMARY KEY(project_id,metric_id,version,ref_metric_id,ref_version),
 FOREIGN KEY(project_id,environment,metric_id,version)
  REFERENCES formula_metric_versions(project_id,environment,metric_id,version),
 FOREIGN KEY(project_id,environment,ref_metric_id,ref_version)
  REFERENCES metric_versions(project_id,environment,metric_id,version)
);

CREATE FUNCTION formula_metric_preserve_scope() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.metric_id IS DISTINCT FROM OLD.metric_id OR NEW.environment IS DISTINCT FROM OLD.environment OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
  RAISE EXCEPTION 'formula metric scope is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER formula_metrics_scope_immutable BEFORE UPDATE ON formula_metrics FOR EACH ROW EXECUTE FUNCTION formula_metric_preserve_scope();
CREATE TRIGGER formula_metric_versions_immutable BEFORE UPDATE OR DELETE ON formula_metric_versions FOR EACH ROW EXECUTE FUNCTION catalog_reject_immutable_change();
CREATE TRIGGER formula_metric_dependencies_immutable BEFORE UPDATE OR DELETE ON formula_metric_dependencies FOR EACH ROW EXECUTE FUNCTION catalog_reject_immutable_change();
