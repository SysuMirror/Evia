-- Dashboard folders are scoped organization metadata with a bounded hierarchy
-- (depth 1-3). They carry no immutable witnesses of their own: empty folders
-- are hard-deleted, while both foreign keys use ON DELETE RESTRICT so a folder
-- that still contains child folders or dashboards can never disappear silently.

CREATE TABLE dashboard_folders (
 project_id uuid NOT NULL REFERENCES projects(id),
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 folder_id uuid NOT NULL,
 parent_id uuid,
 depth smallint NOT NULL CHECK(depth BETWEEN 1 AND 3),
 title text NOT NULL CHECK(length(title) BETWEEN 1 AND 128 AND title=btrim(title)),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision BETWEEN 1 AND 9007199254740991),
 created_by uuid NOT NULL REFERENCES accounts(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(project_id,folder_id),
 UNIQUE(project_id,environment,folder_id),
 -- MATCH SIMPLE skips root rows whose parent_id is NULL. RESTRICT is the
 -- database-level backstop for the empty-only delete rule enforced in Go.
 FOREIGN KEY(project_id,environment,parent_id)
  REFERENCES dashboard_folders(project_id,environment,folder_id)
  MATCH SIMPLE ON DELETE RESTRICT,
 CHECK(parent_id IS DISTINCT FROM folder_id)
);
CREATE INDEX dashboard_folders_scope
 ON dashboard_folders(project_id,environment,parent_id,folder_id);

-- A folder's place in the hierarchy is fixed at creation; only title, revision
-- and updated_at may change afterwards.
CREATE FUNCTION dashboard_folder_preserve_scope() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.project_id IS DISTINCT FROM OLD.project_id
  OR NEW.environment IS DISTINCT FROM OLD.environment
  OR NEW.folder_id IS DISTINCT FROM OLD.folder_id
  OR NEW.parent_id IS DISTINCT FROM OLD.parent_id
  OR NEW.depth IS DISTINCT FROM OLD.depth
  OR NEW.created_at IS DISTINCT FROM OLD.created_at
  OR NEW.created_by IS DISTINCT FROM OLD.created_by THEN
  RAISE EXCEPTION 'dashboard folder scope is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER dashboard_folders_scope_immutable
 BEFORE UPDATE ON dashboard_folders FOR EACH ROW
 EXECUTE FUNCTION dashboard_folder_preserve_scope();

-- Dashboards may live at the root (folder_id NULL) or inside one folder of the
-- same project/environment. RESTRICT keeps folder deletion empty-only.
ALTER TABLE dashboards ADD COLUMN folder_id uuid;
ALTER TABLE dashboards ADD CONSTRAINT dashboards_folder_fkey
 FOREIGN KEY(project_id,environment,folder_id)
 REFERENCES dashboard_folders(project_id,environment,folder_id)
 MATCH SIMPLE ON DELETE RESTRICT;
CREATE INDEX dashboards_folder ON dashboards(project_id,environment,folder_id);
