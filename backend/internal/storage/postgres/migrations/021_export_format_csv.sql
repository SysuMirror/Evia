-- Version 021: widen the export artifact format vocabulary to CSV.
-- Existing json rows stay valid because the check only expands; the default
-- remains json so callers that omit the format keep the original behavior.
ALTER TABLE export_jobs DROP CONSTRAINT export_jobs_format_check;
ALTER TABLE export_jobs ADD CONSTRAINT export_jobs_format_check
 CHECK(format IN ('json','csv'));
