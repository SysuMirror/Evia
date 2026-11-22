-- Version 048: admit every currently implemented ad-hoc query kind, including
-- the bounded heatmap slice. This only widens the closed set; existing jobs and
-- their immutable request payloads are unchanged.
ALTER TABLE analysis_query_jobs DROP CONSTRAINT analysis_query_jobs_kind_check;
ALTER TABLE analysis_query_jobs ADD CONSTRAINT analysis_query_jobs_kind_check
 CHECK(kind IN ('events','trend','journey','funnel','retention','segment','path','attribution','heatmap'));
