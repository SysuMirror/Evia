-- Version 041: record the immutable saved-analysis version that sourced an
-- export job. The new endpoint
-- POST /projects/{id}/saved-analyses/{analysis_id}/versions/{version}/exports
-- binds the published snapshot server-side and materializes it through the
-- existing export_jobs queue. Inline exports keep both columns NULL; the
-- MATCH SIMPLE foreign key skips those rows, and the soundness CHECK requires
-- both witness columns to be null together or populated together. No backfill
-- is needed because historical rows already satisfy the new constraints.
ALTER TABLE export_jobs
 ADD COLUMN source_analysis_id uuid,
 ADD COLUMN source_analysis_version bigint;

ALTER TABLE export_jobs
 ADD CONSTRAINT export_jobs_saved_analysis_source_fkey
  FOREIGN KEY(project_id, source_analysis_id, source_analysis_version)
  REFERENCES saved_analysis_versions(project_id, analysis_id, version) MATCH SIMPLE;

ALTER TABLE export_jobs
 ADD CONSTRAINT export_jobs_source_sound CHECK(
  (source_analysis_id IS NULL AND source_analysis_version IS NULL)
  OR (source_analysis_id IS NOT NULL AND source_analysis_version IS NOT NULL)
 );
