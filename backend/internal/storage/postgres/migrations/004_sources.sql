CREATE TABLE ingestion_sources (
 id uuid PRIMARY KEY,
 project_id uuid NOT NULL REFERENCES projects(id),
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 100),
 kind text NOT NULL CHECK(kind IN ('browser','server')),
 write_key text NOT NULL UNIQUE,
 status text NOT NULL CHECK(status IN ('active','disabled')),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision BETWEEN 1 AND 9007199254740991),
 allowed_origins text[] NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 observed_at timestamptz,
 UNIQUE(project_id,environment,id),
 CHECK(cardinality(allowed_origins) BETWEEN 0 AND 50),
 CHECK(kind!='browser' OR cardinality(allowed_origins)>0),
 CHECK(kind!='server' OR cardinality(allowed_origins)=0)
);
CREATE INDEX ingestion_sources_page ON ingestion_sources(project_id,environment,id);
CREATE TABLE ingestion_credentials (
 id uuid PRIMARY KEY,
 source_id uuid NOT NULL REFERENCES ingestion_sources(id),
 write_key text NOT NULL UNIQUE,
 secret_digest text NOT NULL DEFAULT '',
 secret_ciphertext bytea NOT NULL DEFAULT '',
 expires_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(source_id,id)
);
ALTER TABLE ingestion_credentials ADD CONSTRAINT ingestion_credentials_source_key UNIQUE(source_id,write_key);
ALTER TABLE ingestion_sources ADD CONSTRAINT ingestion_sources_current_credential FOREIGN KEY(id,write_key)
 REFERENCES ingestion_credentials(source_id,write_key) DEFERRABLE INITIALLY DEFERRED;
CREATE INDEX ingestion_credentials_expiry ON ingestion_credentials(expires_at) WHERE expires_at IS NOT NULL;
CREATE TABLE ingestion_rotations (
 actor_id uuid NOT NULL REFERENCES accounts(id),
 project_id uuid NOT NULL,
 environment text NOT NULL,
 source_id uuid NOT NULL,
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 1 AND 128),
 overlap_seconds bigint NOT NULL CHECK(overlap_seconds BETWEEN 0 AND 86400),
 credential_id uuid NOT NULL,
 source_snapshot jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(actor_id,project_id,source_id,idempotency_key),
 FOREIGN KEY(project_id,environment,source_id) REFERENCES ingestion_sources(project_id,environment,id),
 FOREIGN KEY(source_id,credential_id) REFERENCES ingestion_credentials(source_id,id)
);
CREATE FUNCTION ingestion_preserve_source_scope() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.id IS DISTINCT FROM NEW.id OR OLD.project_id IS DISTINCT FROM NEW.project_id
  OR OLD.environment IS DISTINCT FROM NEW.environment OR OLD.kind IS DISTINCT FROM NEW.kind THEN
  RAISE EXCEPTION 'source identity and scope are immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER ingestion_sources_scope BEFORE UPDATE ON ingestion_sources
 FOR EACH ROW EXECUTE FUNCTION ingestion_preserve_source_scope();
