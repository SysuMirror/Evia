CREATE TABLE catalog_events (
 project_id uuid NOT NULL REFERENCES projects(id),
 event_key text NOT NULL CHECK(event_key ~ '^[a-z][a-z0-9_]{0,63}$'),
 event_type_id bigint GENERATED ALWAYS AS IDENTITY UNIQUE CHECK(event_type_id BETWEEN 1 AND 9007199254740991),
 display_name text NOT NULL CHECK(length(display_name) BETWEEN 1 AND 128),
 description text NOT NULL DEFAULT '',
 draft_revision bigint NOT NULL DEFAULT 1 CHECK(draft_revision BETWEEN 1 AND 9007199254740991),
 draft_definition jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(project_id,event_key)
);
-- A release build and collector activation are separate operations. Only a future
-- acknowledged activation may change active_revision; metadata builds never do.
CREATE TABLE catalog_environments (
 project_id uuid NOT NULL REFERENCES projects(id),
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 metadata_revision bigint NOT NULL DEFAULT 0 CHECK(metadata_revision BETWEEN 0 AND 9007199254740991),
 active_revision bigint NOT NULL DEFAULT 0 CHECK(active_revision BETWEEN 0 AND 9007199254740991),
 PRIMARY KEY(project_id,environment)
);
CREATE TABLE catalog_versions (
 project_id uuid NOT NULL,
 event_key text NOT NULL,
 schema_version bigint NOT NULL CHECK(schema_version BETWEEN 1 AND 9007199254740991),
 draft_revision bigint NOT NULL,
 definition jsonb NOT NULL,
 definition_hash text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(project_id,event_key,schema_version),
 UNIQUE(project_id,event_key,definition_hash),
 FOREIGN KEY(project_id,event_key) REFERENCES catalog_events(project_id,event_key)
);
CREATE TABLE catalog_releases (
 id uuid PRIMARY KEY,
 project_id uuid NOT NULL REFERENCES projects(id),
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 revision bigint NOT NULL CHECK(revision BETWEEN 1 AND 9007199254740991),
 state text NOT NULL CHECK(state='published'),
 ingestion_ready boolean NOT NULL DEFAULT false CHECK(NOT ingestion_ready),
 acceleration_state text NOT NULL DEFAULT 'not_requested' CHECK(acceleration_state='not_requested'),
 manifest_hash text NOT NULL,
 manifest jsonb NOT NULL,
 created_by uuid NOT NULL REFERENCES accounts(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(project_id,environment,revision)
);
CREATE INDEX catalog_releases_scope ON catalog_releases(project_id,environment,id);
CREATE INDEX catalog_releases_project ON catalog_releases(project_id,id);
CREATE TABLE catalog_idempotency (
 actor_id uuid NOT NULL REFERENCES accounts(id),
 project_id uuid NOT NULL REFERENCES projects(id),
 environment text NOT NULL,
 operation text NOT NULL,
 idempotency_key text NOT NULL,
 request_hash text NOT NULL,
 release_id uuid NOT NULL REFERENCES catalog_releases(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(actor_id,project_id,environment,operation,idempotency_key)
);
-- This is a durable metadata notification, not an ingestion acknowledgement.
CREATE TABLE catalog_outbox (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 project_id uuid NOT NULL REFERENCES projects(id),
 environment text NOT NULL,
 release_id uuid NOT NULL REFERENCES catalog_releases(id),
 kind text NOT NULL CHECK(kind='catalog.release_built'),
 created_at timestamptz NOT NULL DEFAULT now(),
 delivered_at timestamptz,
 UNIQUE(release_id,kind)
);
CREATE FUNCTION catalog_reject_immutable_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'published catalog metadata is immutable' USING ERRCODE='23514';
END;
$$;
CREATE TRIGGER catalog_versions_immutable BEFORE UPDATE OR DELETE ON catalog_versions
 FOR EACH ROW EXECUTE FUNCTION catalog_reject_immutable_change();
CREATE TRIGGER catalog_releases_immutable BEFORE UPDATE OR DELETE ON catalog_releases
 FOR EACH ROW EXECUTE FUNCTION catalog_reject_immutable_change();
