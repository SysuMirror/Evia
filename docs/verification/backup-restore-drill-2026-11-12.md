# Backup/restore drill verification — 2026-11-12

## Scope

End-to-end verification of `tooling/backup/drill.py` against the real
PostgreSQL 17.9 development container (`evia-development-postgres-1`, host
port 55432). The host has no PostgreSQL client tools, so thin shell wrappers
forwarded `psql`/`pg_dump`/`pg_restore` through `docker exec`, forwarding the
libpq environment variables and syncing dump artifacts back to the host. The
wrappers are local-only shims; the tool itself is unchanged.

## Fixture

An isolated source database `evia_drill_src_d22c2c91b452` was created and the
real `evia migrate` binary applied all **18 embedded migrations** (53 user
tables). Representative control-plane rows were inserted: 2 accounts, 1
project, 1 project membership, 1 audit entry and 1 retention policy row.

## Commands and results

### 1. Full drill (backup → scratch restore → verify → cleanup)

```sh
python3 tooling/backup/drill.py drill \
  --dsn postgres://evia:***@localhost:5432/evia_drill_src_d22c2c91b452?sslmode=disable \
  --out /tmp/evia-drill-out
```

Result: exit 0, `"ok": true`, `findings: []`. The scratch database
`evia_restore_drill_ec5c3bc2e098` was created, restored, verified and dropped
automatically; a follow-up query confirmed zero `evia_restore_drill_*`
databases remained.

The manifest captured all **53 tables** with row counts and content hashes,
all **18 `schema_migrations` ledger entries** with checksums, and the dump
artifact sha256 (208,411 bytes custom-format dump).

### 2. Tamper detection (negative evidence)

A second drill ran with `--keep`; the retained scratch database was then
modified without touching row layout for one table:

* `UPDATE accounts SET display_name='Tampered' WHERE username='bob'` (same row
  count, changed content)
* `DELETE FROM retention_policies` (row removed)

`drill.py verify` exited 1 with exactly the expected findings:

```text
content hash differs for accounts despite equal row counts
row count differs for retention_policies: source=1 target=0
```

This proves the content hash catches in-place tampering that row counts alone
would miss, and the row-count check catches deleted rows.

### 3. Restore target safety guard

Pointing `restore` at the source database name (any name outside the
allowlist) exited 1 before `pg_restore` started:

```text
backup drill failed: restore target database must match
evia_restore_drill_<6-32 lowercase alphanumeric characters>
```

### 4. Pure-logic gate

`make backup-tools-test` runs 20 stdlib-only unit tests covering DSN parsing
and password redaction, libpq env isolation (no credentials in argv), scratch
name allowlist, command construction, identifier guards, snapshot comparison
(table/ledger/count/hash mismatches), manifest round-trip and dump tamper
detection. The target is part of `make check`.

## Cleanup

Both drill databases were dropped, the scratch-database count returned to
zero, and the temporary dump/manifest/report artifacts were removed from the
host. The repository working tree contains only the tool, its tests, the
Makefile target and documentation.

## Limitations

* The drill proves logical backup/restore equivalence for PostgreSQL control
  metadata on a single-node cluster; it does not measure RTO under production
  data volume or cover multi-node HA.
* ClickHouse backup/restore remains a documented manual procedure in the
  [backup/restore runbook](../runbooks/backup-restore.md).
