-- Saved analysis soft lifecycle: an archived analysis leaves the default
-- workspace listing but stays readable by ID, preserving its immutable
-- versions, dependency witnesses and audit history. The scope-immutability
-- trigger (saved_analysis_preserve_scope) only rejects changes to
-- project/analysis/environment/created_at, so archive marker updates and the
-- existing updated_at maintenance are permitted without a trigger change.
ALTER TABLE saved_analyses
 ADD COLUMN archived_at timestamptz NULL,
 ADD COLUMN archived_by uuid NULL REFERENCES accounts(id);

-- Active analyses are the common listing path; archived rows are reached by ID.
CREATE INDEX saved_analyses_active_scope
 ON saved_analyses(project_id, environment, analysis_id)
 WHERE archived_at IS NULL;

-- An analysis cannot be archived twice or restored without an archive marker.
ALTER TABLE saved_analyses
 ADD CONSTRAINT saved_analyses_archive_marker_sound
 CHECK((archived_at IS NULL AND archived_by IS NULL)
    OR (archived_at IS NOT NULL AND archived_by IS NOT NULL));
