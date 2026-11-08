-- Saved analysis drafts are mutable; publication snapshots and their dependency
-- witnesses are immutable facts, independent of current catalog activation.
-- Definition JSON preserves the bounded normalized wire bytes: JSONB adds spaces
-- when rendered, which would incorrectly reject valid inputs near the byte limit.
CREATE TABLE saved_analyses (
 project_id uuid NOT NULL REFERENCES projects(id),
 analysis_id uuid NOT NULL,
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 title text NOT NULL CHECK(length(title) BETWEEN 1 AND 128 AND title=btrim(title)),
 kind text NOT NULL CHECK(kind IN ('trend','funnel')),
 draft_revision bigint NOT NULL DEFAULT 1 CHECK(draft_revision BETWEEN 1 AND 9007199254740991),
 published_version bigint CHECK(published_version BETWEEN 1 AND 9007199254740991),
 definition json NOT NULL CHECK(json_typeof(definition)='object' AND octet_length(definition::text)<=131072),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(project_id,analysis_id),
 UNIQUE(project_id,environment,analysis_id),
 CHECK(definition::jsonb ? 'title' AND json_typeof(definition->'title')='string' AND title=definition->>'title' AND json_typeof(definition->'query')='object' AND (definition->'query')::jsonb ? 'kind' AND kind=definition->'query'->>'kind'),
 CHECK(definition::jsonb ? 'description' AND json_typeof(definition->'description')='string' AND length(definition->>'description')<=2000)
);
CREATE INDEX saved_analyses_scope ON saved_analyses(project_id,environment,analysis_id);
CREATE TABLE saved_analysis_versions (
 project_id uuid NOT NULL,
 analysis_id uuid NOT NULL,
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 version bigint NOT NULL CHECK(version BETWEEN 1 AND 9007199254740991),
 draft_revision bigint NOT NULL CHECK(draft_revision BETWEEN 1 AND 9007199254740991),
 definition json NOT NULL CHECK(json_typeof(definition)='object' AND octet_length(definition::text)<=131072),
 dependencies jsonb NOT NULL CHECK(jsonb_typeof(dependencies)='object' AND octet_length(dependencies::text)<=32768),
 definition_hash text NOT NULL CHECK(definition_hash ~ '^[a-f0-9]{64}$'),
 dependency_hash text NOT NULL CHECK(dependency_hash ~ '^[a-f0-9]{64}$'),
 validation_hash text NOT NULL CHECK(validation_hash ~ '^[a-f0-9]{64}$'),
 created_at timestamptz NOT NULL DEFAULT now(),
 created_by uuid NOT NULL REFERENCES accounts(id),
 PRIMARY KEY(project_id,analysis_id,version),
 UNIQUE(project_id,environment,analysis_id,version),
 FOREIGN KEY(project_id,environment,analysis_id) REFERENCES saved_analyses(project_id,environment,analysis_id)
);
ALTER TABLE saved_analyses ADD CONSTRAINT saved_analyses_published_pointer
 FOREIGN KEY(project_id,environment,analysis_id,published_version)
 REFERENCES saved_analysis_versions(project_id,environment,analysis_id,version) DEFERRABLE INITIALLY DEFERRED;
CREATE TABLE saved_analysis_dependencies (
 project_id uuid NOT NULL,
 environment text NOT NULL,
 analysis_id uuid NOT NULL,
 version bigint NOT NULL,
 event_key text NOT NULL,
 schema_version bigint NOT NULL,
 definition_hash text NOT NULL CHECK(definition_hash ~ '^[a-f0-9]{64}$'),
 release_id uuid NOT NULL,
 PRIMARY KEY(project_id,analysis_id,version,event_key,schema_version),
 FOREIGN KEY(project_id,environment,analysis_id,version) REFERENCES saved_analysis_versions(project_id,environment,analysis_id,version),
 FOREIGN KEY(project_id,event_key,schema_version) REFERENCES catalog_versions(project_id,event_key,schema_version),
 FOREIGN KEY(project_id,environment,event_key,schema_version,release_id) REFERENCES catalog_release_events(project_id,environment,event_key,schema_version,release_id)
);
CREATE TABLE saved_analysis_idempotency (
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
CREATE FUNCTION saved_analysis_preserve_scope() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.analysis_id IS DISTINCT FROM OLD.analysis_id OR NEW.environment IS DISTINCT FROM OLD.environment OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
  RAISE EXCEPTION 'saved analysis scope is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER saved_analyses_scope_immutable BEFORE UPDATE ON saved_analyses FOR EACH ROW EXECUTE FUNCTION saved_analysis_preserve_scope();
CREATE TRIGGER saved_analysis_versions_immutable BEFORE UPDATE OR DELETE ON saved_analysis_versions FOR EACH ROW EXECUTE FUNCTION catalog_reject_immutable_change();
CREATE TRIGGER saved_analysis_dependencies_immutable BEFORE UPDATE OR DELETE ON saved_analysis_dependencies FOR EACH ROW EXECUTE FUNCTION catalog_reject_immutable_change();
CREATE TRIGGER saved_analysis_idempotency_immutable BEFORE UPDATE OR DELETE ON saved_analysis_idempotency FOR EACH ROW EXECUTE FUNCTION catalog_reject_immutable_change();
