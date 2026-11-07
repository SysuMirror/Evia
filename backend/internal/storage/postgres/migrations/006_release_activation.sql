-- Mutable delivery evidence is separate from immutable release manifests.
ALTER TABLE catalog_release_delivery ADD COLUMN next_probe_at timestamptz NOT NULL DEFAULT 'epoch';
CREATE TABLE catalog_release_readiness (
 release_id uuid PRIMARY KEY,
 project_id uuid NOT NULL,
 environment text NOT NULL,
 manifest_hash text NOT NULL CHECK(manifest_hash ~ '^[a-f0-9]{64}$'),
 nonce uuid NOT NULL UNIQUE,
 capability text NOT NULL CHECK(capability='raw-facts-v1'),
 confirmed_at timestamptz NOT NULL,
 ready_until timestamptz NOT NULL,
 FOREIGN KEY(project_id,environment,release_id) REFERENCES catalog_releases(project_id,environment,id),
 CHECK(ready_until>confirmed_at)
);
CREATE TABLE catalog_release_activations (
 release_id uuid PRIMARY KEY,
 project_id uuid NOT NULL,
 environment text NOT NULL,
 revision bigint NOT NULL CHECK(revision BETWEEN 1 AND 9007199254740991),
 activated_at timestamptz NOT NULL DEFAULT now(),
 accept_until timestamptz,
 UNIQUE(project_id,environment,revision),
 FOREIGN KEY(project_id,environment,release_id) REFERENCES catalog_releases(project_id,environment,id)
);
CREATE INDEX catalog_release_activations_compatibility ON catalog_release_activations(project_id,environment,accept_until);
CREATE TABLE delivery_probe_consumptions (
 nonce uuid PRIMARY KEY,
 release_id uuid NOT NULL REFERENCES catalog_releases(id),
 manifest_hash text NOT NULL,
 observed_at timestamptz NOT NULL,
 consumed_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX delivery_probe_consumptions_expiry ON delivery_probe_consumptions(consumed_at);

-- A default release can only advance. Rollback means building a new immutable
-- release revision, preventing revision ABA and stale request overwrites.
CREATE FUNCTION catalog_enforce_activation_monotonic() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.active_revision<OLD.active_revision THEN
  RAISE EXCEPTION 'active release revision cannot decrease' USING ERRCODE='23514';
 END IF;
 IF NEW.active_revision<>OLD.active_revision AND NOT EXISTS (
  SELECT 1 FROM catalog_release_activations a JOIN catalog_release_readiness rd ON rd.release_id=a.release_id
  JOIN catalog_releases r ON r.id=a.release_id
  WHERE a.project_id=NEW.project_id AND a.environment=NEW.environment AND a.revision=NEW.active_revision
   AND rd.manifest_hash=r.manifest_hash AND rd.ready_until>clock_timestamp()
 ) THEN
  RAISE EXCEPTION 'active release requires verified readiness and activation history' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER catalog_environments_activation BEFORE UPDATE OF active_revision ON catalog_environments
 FOR EACH ROW EXECUTE FUNCTION catalog_enforce_activation_monotonic();
