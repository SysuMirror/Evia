-- Migrate applied 002 metadata without changing its checksum or release data.
-- The migration runner executes this file and its ledger entry in one transaction.
-- Keep the existing release/version immutability triggers enabled throughout:
-- copying status and dropping columns are not mutations of a published manifest.
LOCK TABLE catalog_releases, catalog_versions, catalog_idempotency, catalog_outbox IN ACCESS EXCLUSIVE MODE;

ALTER TABLE catalog_releases ADD CONSTRAINT catalog_releases_scope_id_key UNIQUE(project_id,environment,id);

CREATE TABLE catalog_release_events (
 project_id uuid NOT NULL,
 environment text NOT NULL,
 event_key text NOT NULL,
 schema_version bigint NOT NULL,
 release_id uuid NOT NULL,
 PRIMARY KEY(project_id,environment,event_key,schema_version,release_id),
 UNIQUE(release_id,event_key),
 FOREIGN KEY(project_id,environment,release_id) REFERENCES catalog_releases(project_id,environment,id),
 FOREIGN KEY(project_id,event_key,schema_version) REFERENCES catalog_versions(project_id,event_key,schema_version)
);

CREATE TABLE catalog_release_delivery (
 release_id uuid PRIMARY KEY,
 project_id uuid NOT NULL,
 environment text NOT NULL,
 ingestion_ready boolean NOT NULL DEFAULT false,
 acceleration_state text NOT NULL DEFAULT 'not_requested' CHECK(acceleration_state IN ('not_requested','building','ready','failed')),
 updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(project_id,environment,release_id) REFERENCES catalog_releases(project_id,environment,id)
);

-- Reject corrupted legacy manifests instead of silently omitting their members.
DO $$
BEGIN
 IF EXISTS (
  SELECT 1 FROM catalog_releases
  WHERE jsonb_typeof(manifest->'events') IS DISTINCT FROM 'array'
 ) THEN
  RAISE EXCEPTION 'catalog release manifest events must be an array' USING ERRCODE='23514';
 END IF;
 IF EXISTS (
  SELECT 1 FROM catalog_releases
  WHERE jsonb_array_length(manifest->'events') NOT BETWEEN 1 AND 200
 ) THEN
  RAISE EXCEPTION 'catalog release manifest events are outside supported bounds' USING ERRCODE='23514';
 END IF;
END;
$$;

-- The normalized relation is derived only from immutable manifest membership.
-- Foreign keys verify every referenced version belongs to the same project.
INSERT INTO catalog_release_events(project_id,environment,event_key,schema_version,release_id)
 SELECT r.project_id,r.environment,event.event_key,event.schema_version,r.id
 FROM catalog_releases r
 CROSS JOIN LATERAL jsonb_to_recordset(r.manifest->'events') AS event(event_key text,schema_version bigint);

-- Preserve existing operational values before removing their legacy columns.
INSERT INTO catalog_release_delivery(release_id,project_id,environment,ingestion_ready,acceleration_state,updated_at)
 SELECT id,project_id,environment,ingestion_ready,acceleration_state,created_at FROM catalog_releases;

ALTER TABLE catalog_idempotency ADD CONSTRAINT catalog_idempotency_release_scope_fkey
 FOREIGN KEY(project_id,environment,release_id) REFERENCES catalog_releases(project_id,environment,id);
ALTER TABLE catalog_idempotency DROP CONSTRAINT catalog_idempotency_release_id_fkey;
ALTER TABLE catalog_outbox ADD CONSTRAINT catalog_outbox_release_scope_fkey
 FOREIGN KEY(project_id,environment,release_id) REFERENCES catalog_releases(project_id,environment,id);
ALTER TABLE catalog_outbox DROP CONSTRAINT catalog_outbox_release_id_fkey;

ALTER TABLE catalog_releases DROP COLUMN ingestion_ready, DROP COLUMN acceleration_state;

CREATE TRIGGER catalog_release_events_immutable BEFORE UPDATE OR DELETE ON catalog_release_events
 FOR EACH ROW EXECUTE FUNCTION catalog_reject_immutable_change();

-- New rows may only reproduce membership already in the immutable manifest;
-- direct inserts cannot append a different event or version to a built release.
CREATE FUNCTION catalog_validate_release_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS (
  SELECT 1 FROM catalog_releases r, jsonb_array_elements(r.manifest->'events') event
  WHERE r.id=NEW.release_id AND r.project_id=NEW.project_id AND r.environment=NEW.environment
   AND event->>'event_key'=NEW.event_key AND (event->>'schema_version')::bigint=NEW.schema_version
 ) THEN
  RAISE EXCEPTION 'event does not belong to immutable release manifest' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER catalog_release_events_membership BEFORE INSERT ON catalog_release_events
 FOR EACH ROW EXECUTE FUNCTION catalog_validate_release_event();

-- Future collector work may update operational status, but cannot relocate or
-- delete a release's status identity. Metadata builds only create defaults.
CREATE FUNCTION catalog_preserve_delivery_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  RAISE EXCEPTION 'release delivery identity cannot be removed' USING ERRCODE='23514';
 END IF;
 IF OLD.release_id IS DISTINCT FROM NEW.release_id OR OLD.project_id IS DISTINCT FROM NEW.project_id OR OLD.environment IS DISTINCT FROM NEW.environment THEN
  RAISE EXCEPTION 'release delivery identity is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER catalog_release_delivery_identity BEFORE UPDATE OR DELETE ON catalog_release_delivery
 FOR EACH ROW EXECUTE FUNCTION catalog_preserve_delivery_identity();
