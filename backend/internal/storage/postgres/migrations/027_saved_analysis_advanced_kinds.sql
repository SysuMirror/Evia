-- Version 027: allow segment, path and attribution saved analyses.
-- The kind check only expands, so existing trend/funnel/retention rows stay
-- valid without any backfill. Each advanced kind pins its event versions
-- through its typed block (segment event, optional path start, attribution
-- conversion plus touchpoints) rather than top-level event_versions.
ALTER TABLE saved_analyses DROP CONSTRAINT saved_analyses_kind_check;
ALTER TABLE saved_analyses ADD CONSTRAINT saved_analyses_kind_check
 CHECK(kind IN ('trend','funnel','retention','segment','path','attribution'));
