-- Version 016: allow machine principals to hold the data.erasure grant. The
-- version 012 constraint hard-coded the closed permission set and a cap of 24.
-- Drop and recreate the check with the new grant and a cap of 25; existing rows
-- remain valid because containment only grows. The immutable trigger and
-- credential lifecycle are untouched.
ALTER TABLE machine_principals DROP CONSTRAINT machine_principals_permissions_check;
ALTER TABLE machine_principals ADD CONSTRAINT machine_principals_permissions_check
 CHECK(cardinality(permissions) BETWEEN 1 AND 25 AND machine_permissions_unique(permissions)
 AND permissions <@ ARRAY[
  'project.read','catalog.drafts.read','catalog.drafts.write','catalog.releases.read','catalog.releases.publish','catalog.releases.activate',
  'sources.read','sources.write','sources.rotate','queries.read','queries.run','queries.raw.read',
  'analyses.read','analyses.write','analyses.publish','analyses.run',
  'metrics.read','metrics.write','metrics.publish','metrics.run',
  'dashboards.read','dashboards.write','dashboards.publish','dashboards.run',
  'data.erasure']::text[]);
