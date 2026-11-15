-- Dashboard soft lifecycle: an archived dashboard leaves the default workspace
-- listing but stays readable by ID, preserving its immutable versions, runs and
-- audit history. The scope-immutability trigger already permits updating columns
-- other than project/environment/id/created_at and the published pointer, so no
-- trigger change is needed.
ALTER TABLE dashboards
 ADD COLUMN archived_at timestamptz NULL,
 ADD COLUMN archived_by uuid NULL REFERENCES accounts(id);

-- Active dashboards are the common listing path; archived rows are reached by ID.
CREATE INDEX dashboards_active_scope
 ON dashboards(project_id, environment, dashboard_id)
 WHERE archived_at IS NULL;

-- A dashboard cannot be archived twice or restored without an archive marker.
ALTER TABLE dashboards
 ADD CONSTRAINT dashboards_archive_marker_sound
 CHECK((archived_at IS NULL AND archived_by IS NULL)
    OR (archived_at IS NOT NULL AND archived_by IS NOT NULL));
