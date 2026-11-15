-- Composite formula metric soft lifecycle: an archived formula metric leaves
-- the default workspace listing but stays readable by ID, preserving its
-- immutable versions, dependency witnesses and audit history. The scope
-- immutability trigger (formula_metric_preserve_scope) only rejects changes to
-- project/environment/metric_id/created_at, so archive marker updates and the
-- existing updated_at maintenance are permitted without a trigger change.
ALTER TABLE formula_metrics
 ADD COLUMN archived_at timestamptz NULL,
 ADD COLUMN archived_by uuid NULL REFERENCES accounts(id);

-- Active formula metrics are the common listing path; archived rows are reached by ID.
CREATE INDEX formula_metrics_active_scope
 ON formula_metrics(project_id, environment, metric_id)
 WHERE archived_at IS NULL;

-- A formula metric cannot be archived twice or restored without an archive marker.
ALTER TABLE formula_metrics
 ADD CONSTRAINT formula_metrics_archive_marker_sound
 CHECK((archived_at IS NULL AND archived_by IS NULL)
    OR (archived_at IS NOT NULL AND archived_by IS NOT NULL));
