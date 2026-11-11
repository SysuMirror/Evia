# Deployment boundary and operator preparation

The development setup runs a standalone control API and React console from
source. By default `deploy/compose.dev.yaml` starts PostgreSQL; its optional
pipeline profile supplies synthetic backend test dependencies. It is not a
production platform deployment. Use the [development runbook](development.md)
for local setup and PROGRESS.md for each backend module's actual implementation
and verification evidence.

A full stateful Flink topology, production packaging, automated upgrade/rollback,
disaster recovery proof and capacity qualification remain outside this setup.
Persisted control metadata does not confirm event collection or query
availability. Ten million users is a design target; this repository has no
measured production capacity result.

The [disposable dependency profile](dependencies.md) supports real backend
integration tests with Kafka, ClickHouse and Redis. It has deliberately bounded,
ephemeral single-node services and supplies no production availability guarantee.

## Current executable behavior

Build the API with the pinned Go version and build console assets with the pinned
Node/pnpm versions documented in the development runbook. The control setup uses
`evia migrate`, `evia bootstrap` and `evia serve`. Migrations
are embedded, ordered, transactional and recorded with checksums under an
administrative advisory lock. Startup verifies the ledger and refuses a mismatched
schema; it does not perform an implicit upgrade. Use one migration owner and run
bootstrap only once against a fresh account store before accepting requests.

The API requires `EVIA_DATABASE_URL`, `EVIA_PUBLIC_ORIGIN` and a random 32-byte
`EVIA_CURSOR_KEY` encoded as 64 hexadecimal characters. `EVIA_LISTEN` defaults to
127.0.0.1:8080. The database role must have the privileges needed for the chosen
administrative or runtime task; the development Compose role is not a hardened
runtime privilege template. Preserve database credentials and the cursor key in
a secret store. Replacing the cursor key invalidates existing pagination cursors.
Do not commit environment files or pass bootstrap credentials through command-line
arguments. Remove `EVIA_BOOTSTRAP_PASSWORD` from the serving process environment.

An operator preparing a later deployment must provide HTTPS with the console and
`/api` on the exact configured origin, restricted network access to PostgreSQL,
appropriate database TLS, static asset serving, process supervision, durable
storage, backups and an independently verified restore procedure. The API does
not serve the console's `dist` directory. Vite is a local development server.
Only loopback development uses `sslmode=disable` and
`EVIA_ALLOW_INSECURE_HTTP=true`. Keep the latter unset or `false` for HTTPS.
HTTPS configuration uses Secure HttpOnly session cookies, CSRF tokens and Origin
verification; reverse proxies must preserve the intended request origin and must
not expose the local development listeners publicly.

Process health is `/healthz`; database reachability is `/readyz`. Neither endpoint
proves event-pipeline readiness or disaster recovery. Operational metrics are
served unauthenticated at `/metrics` in Prometheus text format; `serve` exposes
them on `EVIA_LISTEN`, while each worker opens a dedicated administrative
listener only when `EVIA_METRICS_LISTEN=host:port` is configured (also serving
its own `/healthz`). Restrict every metrics path to the monitoring network and
follow the [observability runbook](observability.md) for the metric inventory
and scrape configuration. Prometheus SLO recording rules and the critical/
warning alert policy ship under `deploy/monitoring/` with promtool unit tests;
load them into the monitoring Prometheus (scrape jobs must use `evia*` names)
and follow the [SLO and alerting runbook](slo.md) for objectives and response
guidance. A Grafana 11 dashboard JSON is included in the same directory and is
imported through the datasource template variable; Alertmanager routing remains
site-specific. Use UTC timestamps for storage and IANA project
timezones such as `Asia/Shanghai` for business reporting boundaries. The Go
binary embeds timezone data.

## ClickHouse read-layout upgrades

`evia migrate` manages PostgreSQL only. Apply the ordered scripts in
`deploy/clickhouse` explicitly to the intended ClickHouse database. Current
query workers require migration 004's compact winner columns; migration 005
adds the identity projections used to reduce candidate scans. Read access to
both `event_facts` and `event_facts_raw` is required for the query role.

The [compact-column upgrade](compact-winner-upgrade.md) and
[identity-projection upgrade](identity-query-upgrade.md) separate additive DDL
from historical materialization. Inspect disk reserves and unfinished mutations,
materialize one inspected partition at a time, and wait for completion before
claiming the new read cost. Unmaterialized historical data remains correct but
can exceed the unchanged query budgets. Keep original columns and the reference
view available during reader rollback. No property-column migration is shipped.

## Evidence required before expanding deployment scope

Apply changes first to a disposable or staging database, run the actual
PostgreSQL and browser commands in the development runbook, and verify schema
checksums before serving. Record the exact application revision, dependency
versions, environment, test results and remaining limitations. A safe upgrade
process also needs backups/restores tested against the intended storage, explicit
migration compatibility decisions and a reviewed rollback or forward-repair plan.
Automated PostgreSQL backup, scratch-restore and three-axis verification
(per-table counts, content hashes, migration ledger) ship in
`tooling/backup/drill.py` with the [backup/restore runbook](backup-restore.md)
and a recorded real-container drill; ClickHouse backup remains a documented
manual procedure.

Public business-integration skills and their release packaging are a separate
future deliverable. A public package needs an explicit file allowlist, versioning
and compatibility checks; internal engineering instructions are not integration
assets and must never be distributed or written into a customer's repository.
