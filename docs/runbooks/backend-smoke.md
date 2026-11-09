# Real HTTP backend smoke

`tooling/verification/backend_smoke.py` exercises an already running local API
and actual PostgreSQL, Redis, Kafka and ClickHouse dependencies with the ingest,
query and release-delivery workers. It uses only documented HTTP APIs. It never
updates SQL readiness flags, writes directly to ClickHouse, substitutes a queue,
starts services or runs application migrations.

Wait for the runtime and activation owners to report readiness, and use the same
frozen executable revision for the API and workers. The operator must explicitly
provision synthetic topics and explicitly apply the sorted SQL migration chain
under `deploy/clickhouse` to the intended local database before starting those
processes. The
[dependency runbook](dependencies.md) documents the disposable containers and
their limits. Creating containers does not establish that the application chain
works, and this script does not prove production replication or capacity.

The current executable requires the PostgreSQL migration ledger through `008`.
Apply migrations explicitly before starting it. Migrations `007` and `008` add
saved analysis versions and bounded execution retry metadata; they preserve
existing event facts, catalog releases and query jobs.

The API must enable collection and analysis, use the matching source-secret key,
and share the real dependencies with all workers. The development environment
uses `evia.dev.events`, `evia.dev.quarantine`, the reserved
`evia.dev.readiness` topic, group `evia-dev-materializer` and ClickHouse database
`evia_dev`. Provisioning and process lifecycle remain explicit operator actions.

Apply the full ordered migration chain before starting the current runtime.
`004_compact_winner_columns.sql` is required by query readers, and
`005_identity_projections.sql` adds selective identity access paths. Historical
materialization is separate from DDL; follow the
[compact-column](compact-winner-upgrade.md) and
[identity-projection](identity-query-upgrade.md) procedures. The synthetic runtime
preparation helper applies DDL explicitly, but never rewrites historical parts.

`003_event_precision.sql` is required by the ingest sink. It adds two
nullable DateTime64(9) payload columns and replaces the exact view aliases while
retaining the existing sorting-key layout. The view coalesces omitted new fields
to the legacy timestamps, preserving compatibility with old writers, including
JSONEachRow omitted-field defaults; rollback to an old writer remains possible. Existing
millisecond history remains at millisecond precision: lost historical nanoseconds
cannot be reconstructed. The two additional 8-byte timestamp payloads and null
maps increase per-row storage before compression. The new sink rejects unknown
fields, so missing migration 003 fails closed. Keep `001_event_facts.sql` immutable and use
the migration chain for upgrades.

Run Python 3.12 or newer from the repository root. Supply the dedicated synthetic
account through `EVIA_E2E_USERNAME` and `EVIA_E2E_PASSWORD` in the private process
environment. Do not place passwords in command arguments, source files or output.
`EVIA_PUBLIC_ORIGIN` must match the API's configured management origin. With the
environment already loaded privately, the actual smoke command is:

```bash
python3 tooling/verification/backend_smoke.py --api-url http://127.0.0.1:8080 --console-origin http://127.0.0.1:5173 --timeout 60
```

`EVIA_E2E_API_URL` can provide the default API origin. The script only accepts
loopback destinations and refuses missing credentials. It uses a session cookie
jar and CSRF token for management, with an entirely separate client carrying no
cookies for public configuration and collection. Redirects are rejected.

Each run creates a new synthetic project and publishes a bounded custom
`smoke_page_view` definition allowed for browser and server sources. This selected
page event supplies the count/PV fixture; it does not claim automatic system-event
instrumentation or identity resolution. The script:

1. Logs in, creates the project, saves and validates its draft, and publishes it.
2. Creates browser/server sources, waits for actual delivery readiness, activates
   the release and reads both public configurations, including ETag responses.
3. Submits a browser batch containing one valid event and one invalid sibling,
   then retries that exact batch. The valid sibling must be acknowledged and the
   invalid sibling permanently rejected both times.
4. Submits a second browser event with another UID and a server event reusing the
   first event ID. It verifies rejection of a foreign Origin and wrong bearer.
5. Creates real query jobs, polls each job to completion, and repeats a completed
   snapshot only while ingestion is not yet visible. It requires exactly three
   source-scoped events and two exact raw UIDs. The total query-job budget is 32.
6. Reads a raw UID journey and requires the two source/event-ID pairs, their
   ordering and preserved context link. An exact retry must not add a third row.
7. Creates a saved analysis with exact event-version pins and count/UID metrics,
   validates without execution, publishes two versions, and checks stale writes,
   original-response retries, immutable old versions and paginated history.
8. Executes version one after version two is published. The result must remain
   three events and two UIDs. The same execution key must reuse its job, report
   its current completion state and reject reuse against another version.
9. Disables both sources, waits for their application state and at least the
   two-second cache window, then requires `SOURCE_DISABLED` for new submissions.
   A final real query must still return count three and UID unique two.
10. Revokes its management session.

Readiness, queue visibility and query jobs use bounded polling. Unexpected HTTP
errors, failed jobs, excessive counts, missing rows or exhausted budgets fail the
run; there is no automatic repair or weaker fallback. Fix the responsible module
and run again with a fresh project after reviewing the failure. Do not mark a
release ready or alter a result directly to get a passing smoke.

Output is bounded JSON evidence containing stage names, resource identifiers and
counts. It omits credentials, cookie/CSRF values and event/query result payloads.
The new project and immutable release remain available for inspection; successful
runs leave both sources disabled. Failed runs can leave a partial synthetic
project. This runbook and script are not themselves evidence of a successful
runtime smoke: record the actual command, executable revision and output in the
module's verification record after it runs.
