-- Version 020: allow machine principals to hold the data.export grant.
-- Migration 016 hard-coded the closed permission set with a cap of 25. Grow
-- containment to 26 entries; existing rows stay valid because the check only
-- expands. The immutable uniqueness trigger and credential lifecycle are
-- untouched.
ALTER TABLE machine_principals DROP CONSTRAINT machine_principals_permissions_check;
ALTER TABLE machine_principals ADD CONSTRAINT machine_principals_permissions_check
 CHECK(cardinality(permissions) BETWEEN 1 AND 26 AND machine_permissions_unique(permissions)
 AND permissions <@ ARRAY[
  'project.read','catalog.drafts.read','catalog.drafts.write','catalog.releases.read','catalog.releases.publish','catalog.releases.activate',
  'sources.read','sources.write','sources.rotate','queries.read','queries.run','queries.raw.read',
  'analyses.read','analyses.write','analyses.publish','analyses.run',
  'metrics.read','metrics.write','metrics.publish','metrics.run',
  'dashboards.read','dashboards.write','dashboards.publish','dashboards.run',
  'data.erasure','data.export']::text[]);
