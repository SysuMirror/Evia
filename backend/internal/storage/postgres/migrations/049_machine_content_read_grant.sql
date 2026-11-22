-- Version 049: keep PostgreSQL's closed machine grant vocabulary aligned with
-- the domain/OpenAPI content.read permission introduced by the protected
-- content API. Existing rows remain valid because this only expands the set.
ALTER TABLE machine_principals DROP CONSTRAINT machine_principals_permissions_check;
ALTER TABLE machine_principals ADD CONSTRAINT machine_principals_permissions_check
 CHECK(cardinality(permissions) BETWEEN 1 AND 27 AND machine_permissions_unique(permissions)
 AND permissions <@ ARRAY[
  'project.read','catalog.drafts.read','catalog.drafts.write','catalog.releases.read','catalog.releases.publish','catalog.releases.activate',
  'sources.read','sources.write','sources.rotate','queries.read','queries.run','queries.raw.read',
  'analyses.read','analyses.write','analyses.publish','analyses.run',
  'metrics.read','metrics.write','metrics.publish','metrics.run',
  'dashboards.read','dashboards.write','dashboards.publish','dashboards.run',
  'data.erasure','data.export','content.read']::text[]);
