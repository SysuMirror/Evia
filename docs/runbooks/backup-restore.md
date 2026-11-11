# Backup and restore runbook

Evia has two durable stores with different recovery profiles:

| Store | Contents | Loss impact | Recovery path |
| --- | --- | --- | --- |
| PostgreSQL | Accounts, projects, governance, saved analyses, dashboards, metrics, machine credentials, **erasure tombstones**, retention policy and the migration ledger | Irreplaceable control and compliance state | Automated backup/restore drill in this runbook |
| ClickHouse | Event facts and projections | Re-ingestable from Kafka while retention allows; historical gaps are permanent once Kafka data ages out | Manual `BACKUP`/`RESTORE` procedure below |

The automation lives in [`tooling/backup/drill.py`](../../tooling/backup/drill.py)
and uses only the Python standard library plus the local PostgreSQL client
tools. It never accepts credentials on the command line: libpq environment
variables (`PGPASSWORD`) carry the password, and every DSN written into a
manifest or report is password-redacted.

## Prerequisites

* `pg_dump`, `pg_restore` and `psql` installed on the operator host, matching
  the server major version (the pinned development stack is PostgreSQL 17).
* Network reachability to the database and a role allowed to run `pg_dump`
  and, on the drill target, `CREATE DATABASE`.
* An output directory on storage encrypted at rest and covered by the site's
  retention policy.

## Commands

```sh
# 1. Backup: custom-format dump plus a signed manifest with sha256, row
#    counts, per-table content hashes and the migration ledger snapshot.
python3 tooling/backup/drill.py backup \
  --dsn "$EVIA_DATABASE_URL" --out /var/backups/evia

# 2. Snapshot one database as JSON (useful for ad hoc comparisons).
python3 tooling/backup/drill.py snapshot --dsn "$EVIA_DATABASE_URL"

# 3. Full drill: backup -> isolated scratch database -> restore -> verify.
#    The scratch database is created and dropped automatically.
python3 tooling/backup/drill.py drill \
  --dsn "$EVIA_DATABASE_URL" --out /var/backups/evia

# 4. Verify an existing restore independently (exit 1 on any mismatch).
python3 tooling/backup/drill.py verify \
  --source-dsn "$EVIA_DATABASE_URL" --target-dsn "$SCRATCH_DSN"

# 5. Manual restore into an explicitly named scratch database. The target
#    name must match evia_restore_drill_<6-32 lowercase alphanumerics>; any
#    other database is rejected before pg_restore starts.
python3 tooling/backup/drill.py restore \
  --dsn "$SCRATCH_DSN" --artifact /var/backups/evia/evia-pg-backup-*.dump \
  --manifest /var/backups/evia/evia-pg-backup-*.manifest.json
```

## Artifacts and verification

Each backup produces two files:

* `evia-pg-backup-<UTC stamp>.dump` — `pg_dump -Fc` custom format, created with
  `--no-owner --no-privileges` so cluster role differences never block restore.
* `evia-pg-backup-<UTC stamp>.manifest.json` — dump size, sha256, redacted
  source DSN, and a snapshot containing every user table's row count and
  `hashtextextended` content hash plus the full `schema_migrations` ledger.

`drill` restores into a brand-new database and compares both databases on
three axes: missing/extra tables, per-table row counts plus content hashes,
and exact migration ledger names/checksums. Any difference makes the command
exit 1 and writes a structured report
(`evia-restore-drill-<UTC stamp>.report.json`) listing every finding. A
manifest whose dump checksum no longer matches is rejected before restore.

## Safety model

* Restore targets must match the `evia_restore_drill_` allowlist; the tool can
  never point `pg_restore` at a production database name.
* `pg_restore` runs with `--clean --if-exists --exit-on-error`, making the
  scratch restore idempotent while surfacing every DDL/data error instead of
  demoting it to a warning.
* Scratch databases are dropped at the end of every drill; `--keep` retains
  one for manual inspection and must be cleaned up explicitly.
* SQL identifiers used in generated queries pass a strict snake_case
  allowlist before reaching psql.

## Scheduling and recovery objectives (initial policy)

These are starting targets, not measured results; tune them after the first
real cycles and record the change here.

* Backups: nightly logical backup, retained for at least 14 days with a
  weekly copy held off-host for 90 days.
* Drill: run `drill` against a staging cluster at least weekly and after every
  PostgreSQL major-version upgrade; archive the report.
* RPO target: 24 hours for control metadata (bounded by the backup interval).
* RTO target: 4 hours from declaring a recovery event to a verified restore.

## Disaster recovery procedure

1. Provision an empty PostgreSQL instance and apply the same role/credentials.
2. Select the newest manifest whose sha256 matches its dump.
3. Create a scratch database matching the allowlist and run `restore` with
   the artifact and manifest.
4. Run `verify` against the old source (if still reachable) or compare the
   restored snapshot against the manifest snapshot.
5. Only after verification, redirect the application `EVIA_DATABASE_URL` to
   the restored instance. Restoring directly over a live production database
   is intentionally outside the tool's scope.

## ClickHouse event data

Event facts are derived data and the automated drill deliberately covers
PostgreSQL only. For ClickHouse, use the server's native backup feature on a
maintenance window:

```sql
BACKUP TABLE evia.event_facts, evia.event_facts_raw
TO File('evia-facts-<UTC stamp>') SETTINGS compression_method='zstd';
-- On the replacement cluster:
RESTORE TABLE evia.event_facts, evia.event_facts_raw
FROM File('evia-facts-<UTC stamp>');
```

Object storage (`S3`/`GCS` destinations) replaces `File` in multi-node
deployments; automate and drill this separately when the ClickHouse topology
is finalized. Erasure tombstones live in PostgreSQL, so a verified PG restore
always preserves the resurrection barrier even when event facts are rebuilt
from Kafka rather than restored.

## Local container verification

The development Compose stack runs PostgreSQL inside a container without a
host-local client toolchain. The recorded verification was run with thin
shell wrappers that forward `psql`/`pg_dump`/`pg_restore` through
`docker exec` (forwarding the libpq environment variables and syncing dump
artifacts back to the host for checksumming). The wrappers are local test
shims, not repository assets; production deployments install the client
tools directly.
