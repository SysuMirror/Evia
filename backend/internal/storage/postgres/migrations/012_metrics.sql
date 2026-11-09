-- Metric drafts are mutable; publication snapshots and their dependency
-- witnesses are immutable facts, independent of current catalog activation.
-- Definition JSON preserves the bounded normalized wire bytes: JSONB adds spaces
-- when rendered, which would incorrectly reject valid inputs near the byte limit.
CREATE TABLE metrics (
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
 CHECK(definition::jsonb ? 'title' AND json_typeof(definition->'title')='string' AND title=definition->>'title' AND json_typeof(definition->'event_versions')='array' AND json_array_length(definition->'event_versions') BETWEEN 1 AND 20 AND json_typeof(definition->'aggregation')='object'),
 CHECK(definition::jsonb ? 'description' AND json_typeof(definition->'description')='string' AND length(definition->>'description')<=2000)
);
CREATE INDEX metrics_scope ON metrics(project_id,environment,metric_id);
CREATE TABLE metric_versions (
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
 FOREIGN KEY(project_id,environment,metric_id) REFERENCES metrics(project_id,environment,metric_id)
);
ALTER TABLE metrics ADD CONSTRAINT metrics_published_pointer
 FOREIGN KEY(project_id,environment,metric_id,published_version)
 REFERENCES metric_versions(project_id,environment,metric_id,version) DEFERRABLE INITIALLY DEFERRED;
CREATE TABLE metric_dependencies (
 project_id uuid NOT NULL,
 environment text NOT NULL,
 metric_id uuid NOT NULL,
 version bigint NOT NULL,
 event_key text NOT NULL,
 schema_version bigint NOT NULL,
 definition_hash text NOT NULL CHECK(definition_hash ~ '^[a-f0-9]{64}$'),
 release_id uuid NOT NULL,
 PRIMARY KEY(project_id,metric_id,version,event_key,schema_version),
 FOREIGN KEY(project_id,environment,metric_id,version) REFERENCES metric_versions(project_id,environment,metric_id,version),
 FOREIGN KEY(project_id,event_key,schema_version) REFERENCES catalog_versions(project_id,event_key,schema_version),
 FOREIGN KEY(project_id,environment,event_key,schema_version,release_id) REFERENCES catalog_release_events(project_id,environment,event_key,schema_version,release_id)
);
CREATE TABLE metric_idempotency (
 actor_id uuid NOT NULL REFERENCES principals(id),
 project_id uuid NOT NULL REFERENCES projects(id),
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 operation text NOT NULL,
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 1 AND 128),
 request_hash text NOT NULL CHECK(request_hash ~ '^[a-f0-9]{64}$'),
 response jsonb NOT NULL CHECK(jsonb_typeof(response)='object' AND octet_length(response::text)<=196608),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(actor_id,project_id,environment,operation,idempotency_key)
);
CREATE FUNCTION metric_preserve_scope() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.metric_id IS DISTINCT FROM OLD.metric_id OR NEW.environment IS DISTINCT FROM OLD.environment OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
  RAISE EXCEPTION 'metric scope is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER metrics_scope_immutable BEFORE UPDATE ON metrics FOR EACH ROW EXECUTE FUNCTION metric_preserve_scope();
CREATE TRIGGER metric_versions_immutable BEFORE UPDATE OR DELETE ON metric_versions FOR EACH ROW EXECUTE FUNCTION catalog_reject_immutable_change();
CREATE TRIGGER metric_dependencies_immutable BEFORE UPDATE OR DELETE ON metric_dependencies FOR EACH ROW EXECUTE FUNCTION catalog_reject_immutable_change();
CREATE TRIGGER metric_idempotency_immutable BEFORE UPDATE OR DELETE ON metric_idempotency FOR EACH ROW EXECUTE FUNCTION catalog_reject_immutable_change();

CREATE TABLE metric_query_requests (
 actor_id uuid NOT NULL REFERENCES principals(id),
 project_id uuid NOT NULL REFERENCES projects(id),
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 metric_id uuid NOT NULL,
 version bigint NOT NULL CHECK(version BETWEEN 1 AND 9007199254740991),
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 1 AND 128),
 request_hash text NOT NULL CHECK(request_hash ~ '^[a-f0-9]{64}$'),
 query_id uuid,
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL DEFAULT now()+interval '7 days',
 PRIMARY KEY(actor_id,project_id,environment,metric_id,idempotency_key),
 FOREIGN KEY(project_id,environment,metric_id,version)
  REFERENCES metric_versions(project_id,environment,metric_id,version),
 FOREIGN KEY(project_id,environment,query_id)
  REFERENCES analysis_query_jobs(project_id,environment,id) ON DELETE SET NULL(query_id),
 CHECK(expires_at>created_at)
);
CREATE INDEX metric_query_requests_expiry
 ON metric_query_requests(project_id,expires_at);
CREATE INDEX metric_query_requests_job
 ON metric_query_requests(project_id,environment,query_id) WHERE query_id IS NOT NULL;

-- Extend the allowlist without granting any existing principal new authority.
ALTER TABLE machine_principals DROP CONSTRAINT machine_principals_permissions_check;
ALTER TABLE machine_principals ADD CONSTRAINT machine_principals_permissions_check
 CHECK(cardinality(permissions) BETWEEN 1 AND 24 AND machine_permissions_unique(permissions)
 AND permissions <@ ARRAY['project.read','catalog.drafts.read','catalog.drafts.write','catalog.releases.read','catalog.releases.publish','catalog.releases.activate','sources.read','sources.write','sources.rotate','queries.read','queries.run','queries.raw.read','analyses.read','analyses.write','analyses.publish','analyses.run','dashboards.read','dashboards.write','dashboards.publish','dashboards.run','metrics.read','metrics.write','metrics.publish','metrics.run']::text[]);
