-- Preserve historical human identifiers while making machine actors independent.
CREATE TABLE principals (
 id uuid PRIMARY KEY,
 kind text NOT NULL CHECK(kind IN ('human','machine'))
);
INSERT INTO principals(id,kind) SELECT id,'human' FROM accounts;
CREATE FUNCTION account_register_principal() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO principals(id,kind) VALUES(NEW.id,'human');
 RETURN NEW;
END;
$$;
CREATE TRIGGER account_principal AFTER INSERT ON accounts FOR EACH ROW EXECUTE FUNCTION account_register_principal();
CREATE FUNCTION principal_identity_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'principal identity is immutable' USING ERRCODE='23514';
END;
$$;
CREATE TRIGGER principal_identity_immutable BEFORE UPDATE OR DELETE ON principals FOR EACH ROW EXECUTE FUNCTION principal_identity_immutable();

ALTER TABLE audit_entries DROP CONSTRAINT audit_entries_actor_id_fkey;
ALTER TABLE audit_entries ADD FOREIGN KEY(actor_id) REFERENCES principals(id);
ALTER TABLE catalog_releases DROP CONSTRAINT catalog_releases_created_by_fkey;
ALTER TABLE catalog_releases ADD FOREIGN KEY(created_by) REFERENCES principals(id);
ALTER TABLE catalog_idempotency DROP CONSTRAINT catalog_idempotency_actor_id_fkey;
ALTER TABLE catalog_idempotency ADD FOREIGN KEY(actor_id) REFERENCES principals(id);
ALTER TABLE ingestion_rotations DROP CONSTRAINT ingestion_rotations_actor_id_fkey;
ALTER TABLE ingestion_rotations ADD FOREIGN KEY(actor_id) REFERENCES principals(id);
ALTER TABLE analysis_query_jobs DROP CONSTRAINT analysis_query_jobs_actor_id_fkey;
ALTER TABLE analysis_query_jobs ADD FOREIGN KEY(actor_id) REFERENCES principals(id);
ALTER TABLE saved_analysis_versions DROP CONSTRAINT saved_analysis_versions_created_by_fkey;
ALTER TABLE saved_analysis_versions ADD FOREIGN KEY(created_by) REFERENCES principals(id);
ALTER TABLE saved_analysis_idempotency DROP CONSTRAINT saved_analysis_idempotency_actor_id_fkey;
ALTER TABLE saved_analysis_idempotency ADD FOREIGN KEY(actor_id) REFERENCES principals(id);
ALTER TABLE saved_analysis_query_requests DROP CONSTRAINT saved_analysis_query_requests_actor_id_fkey;
ALTER TABLE saved_analysis_query_requests ADD FOREIGN KEY(actor_id) REFERENCES principals(id);
ALTER TABLE dashboard_versions DROP CONSTRAINT dashboard_versions_created_by_fkey;
ALTER TABLE dashboard_versions ADD FOREIGN KEY(created_by) REFERENCES principals(id);
ALTER TABLE dashboard_idempotency DROP CONSTRAINT dashboard_idempotency_actor_id_fkey;
ALTER TABLE dashboard_idempotency ADD FOREIGN KEY(actor_id) REFERENCES principals(id);
ALTER TABLE dashboard_runs DROP CONSTRAINT dashboard_runs_created_by_fkey;
ALTER TABLE dashboard_runs ADD FOREIGN KEY(created_by) REFERENCES principals(id);

CREATE FUNCTION machine_permissions_unique(text[]) RETURNS boolean LANGUAGE sql IMMUTABLE STRICT AS $$
 SELECT cardinality($1)=count(DISTINCT permission) FROM unnest($1) AS permission;
$$;
CREATE TABLE machine_principals (
 id uuid PRIMARY KEY REFERENCES principals(id),
 project_id uuid NOT NULL REFERENCES projects(id),
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 128 AND name=btrim(name)),
 permissions text[] NOT NULL CHECK(cardinality(permissions) BETWEEN 1 AND 20 AND machine_permissions_unique(permissions)
  AND permissions <@ ARRAY['project.read','catalog.drafts.read','catalog.drafts.write','catalog.releases.read','catalog.releases.publish','catalog.releases.activate','sources.read','sources.write','sources.rotate','queries.read','queries.run','queries.raw.read','analyses.read','analyses.write','analyses.publish','analyses.run','dashboards.read','dashboards.write','dashboards.publish','dashboards.run']::text[]),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision BETWEEN 1 AND 9007199254740991),
 created_by uuid NOT NULL REFERENCES accounts(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 revoked_at timestamptz,
 UNIQUE(id,project_id)
);
CREATE INDEX machine_principals_scope ON machine_principals(project_id,id);
CREATE FUNCTION machine_principal_immutable_grants() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'machine principal cannot be deleted' USING ERRCODE='23514'; END IF;
 IF (NEW.id,NEW.project_id,NEW.environment,NEW.name,NEW.permissions,NEW.created_by,NEW.created_at)
  IS DISTINCT FROM (OLD.id,OLD.project_id,OLD.environment,OLD.name,OLD.permissions,OLD.created_by,OLD.created_at)
  OR NEW.revision<>OLD.revision+1 OR (OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS DISTINCT FROM OLD.revoked_at) THEN
  RAISE EXCEPTION 'machine identity and grants are immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER machine_principal_immutable_grants BEFORE UPDATE OR DELETE ON machine_principals FOR EACH ROW EXECUTE FUNCTION machine_principal_immutable_grants();
CREATE TABLE machine_credentials (
 id uuid PRIMARY KEY,
 principal_id uuid NOT NULL REFERENCES machine_principals(id),
 secret_digest text NOT NULL UNIQUE CHECK(secret_digest ~ '^[a-f0-9]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL CHECK(expires_at>created_at),
 revoked_at timestamptz,
 UNIQUE(id,principal_id)
);
CREATE INDEX machine_credentials_principal ON machine_credentials(principal_id,created_at,id);
CREATE FUNCTION machine_credential_permanent() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'machine credential cannot be deleted' USING ERRCODE='23514'; END IF;
 IF (NEW.id,NEW.principal_id,NEW.secret_digest,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.principal_id,OLD.secret_digest,OLD.created_at)
  OR NEW.expires_at>OLD.expires_at OR (OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS DISTINCT FROM OLD.revoked_at) THEN
  RAISE EXCEPTION 'machine credential cannot be revived' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER machine_credential_permanent BEFORE UPDATE OR DELETE ON machine_credentials FOR EACH ROW EXECUTE FUNCTION machine_credential_permanent();
CREATE TABLE machine_idempotency (
 actor_id uuid NOT NULL REFERENCES accounts(id),
 project_id uuid NOT NULL REFERENCES projects(id),
 operation text NOT NULL,
 idempotency_key text NOT NULL CHECK(idempotency_key ~ '^[!-~]{1,128}$'),
 request_hash text NOT NULL CHECK(request_hash ~ '^[a-f0-9]{64}$'),
 principal_id uuid NOT NULL,
 credential_id uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(actor_id,project_id,operation,idempotency_key),
 FOREIGN KEY(principal_id,project_id) REFERENCES machine_principals(id,project_id),
 FOREIGN KEY(credential_id,principal_id) REFERENCES machine_credentials(id,principal_id)
);
ALTER TABLE analysis_query_jobs ADD COLUMN credential_id uuid;
ALTER TABLE analysis_query_jobs ADD FOREIGN KEY(credential_id,actor_id) REFERENCES machine_credentials(id,principal_id);
ALTER TABLE analysis_query_jobs ADD COLUMN requires_raw boolean NOT NULL DEFAULT false;
UPDATE analysis_query_jobs SET requires_raw=true WHERE kind IN ('events','journey')
 OR request ? 'identity_filter'
 OR request @? '$.** ? (@.kind == "system" && (@.key == "uid" || @.key == "anonymous_id" || @.key == "session_id" || @.key == "page_view_id" || @.key == "operation_id"))';
ALTER TABLE audit_entries ADD COLUMN credential_id uuid;
ALTER TABLE audit_entries ADD FOREIGN KEY(credential_id,actor_id) REFERENCES machine_credentials(id,principal_id);
CREATE FUNCTION audit_machine_credential() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE actor_kind text;
BEGIN
 SELECT kind INTO actor_kind FROM principals WHERE id=NEW.actor_id;
 NEW.credential_id:=NULLIF(current_setting('evia.credential_id',true),'')::uuid;
 IF (actor_kind='machine') IS DISTINCT FROM (NEW.credential_id IS NOT NULL) THEN
  RAISE EXCEPTION 'audit actor credential does not match identity kind' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER audit_machine_credential BEFORE INSERT ON audit_entries FOR EACH ROW EXECUTE FUNCTION audit_machine_credential();
