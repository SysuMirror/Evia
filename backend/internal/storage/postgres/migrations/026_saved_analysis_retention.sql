-- Version 026: allow retention saved analyses.
-- The kind check only expands, so existing trend/funnel rows stay valid without
-- any backfill. Retention templates pin their two boundary event steps through
-- the typed retention block rather than top-level event_versions.
ALTER TABLE saved_analyses DROP CONSTRAINT saved_analyses_kind_check;
ALTER TABLE saved_analyses ADD CONSTRAINT saved_analyses_kind_check
 CHECK(kind IN ('trend','funnel','retention'));
