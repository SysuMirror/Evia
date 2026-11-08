# Verify the complete console

The event, release, source and analysis workspaces require the real control API,
PostgreSQL, Redis, Kafka, ClickHouse, ingest worker and query worker. The API runs
the delivery probe coordinator when its probe topic is configured. A PostgreSQL-only
server supports access pages but cannot validate this complete workflow.

Use a dedicated disposable PostgreSQL database and synthetic account, following
the [development setup](development.md). Never point verification at production.
Keep `EVIA_DATABASE_URL`, `EVIA_CURSOR_KEY` and any reused `EVIA_SOURCE_SECRET_KEY`
private. When reusing a bootstrapped database, provide its existing
`EVIA_E2E_USERNAME` and `EVIA_E2E_PASSWORD`; bootstrap must not be repeated.

## Prepare dependencies and private configuration

From the repository root, choose fresh private configuration paths outside the
checkout. The helpers refuse to overwrite an existing configuration:

```bash
./deploy/dev-env init /tmp/evia-console-dependencies.env
./deploy/dev-env up /tmp/evia-console-dependencies.env kafka clickhouse redis
./deploy/dev-env check /tmp/evia-console-dependencies.env kafka clickhouse redis
python3 tooling/verification/browser_runtime.py init /tmp/evia-console-dependencies.env /tmp/evia-console-runtime.env
python3 tooling/verification/browser_runtime.py prepare /tmp/evia-console-dependencies.env /tmp/evia-console-runtime.env
```

`init` uses the configured PostgreSQL URL and produces mode-0600 runtime settings,
including a unique Kafka/ClickHouse namespace, private source key and synthetic
account credentials. `prepare` explicitly creates the dedicated topics and
applies the checked-in ClickHouse migrations in order with a checksum ledger.
It never inserts event facts, query results or readiness markers. Repeating
`prepare` on the same private pair checks the ledger; changed historical migration
content fails. The dependency profile is disposable and has the limits described
in [dependencies](dependencies.md).

Build one executable for all processes:

```bash
mkdir -p bin
go -C backend build -o "$PWD/bin/evia" ./cmd/evia
python3 tooling/verification/browser_runtime.py run /tmp/evia-console-runtime.env -- ./bin/evia migrate
# Only for a new, empty account store:
python3 tooling/verification/browser_runtime.py run /tmp/evia-console-runtime.env -- ./bin/evia bootstrap
```

In separate terminals, start each process with that same private configuration:

```bash
python3 tooling/verification/browser_runtime.py run /tmp/evia-console-runtime.env -- ./bin/evia serve
python3 tooling/verification/browser_runtime.py run /tmp/evia-console-runtime.env -- ./bin/evia ingest-worker
python3 tooling/verification/browser_runtime.py run /tmp/evia-console-runtime.env -- ./bin/evia query-worker
```

Wait for `/readyz` on `http://127.0.0.1:8080` to return 204. Keep processes running
while testing. Use the exact console origin `http://127.0.0.1:5173` and the existing
browser dependency installation from the development instructions.

## Run the verification

```bash
pnpm test:client
python3 tooling/verification/browser_runtime.py run /tmp/evia-console-runtime.env -- pnpm test:browser
```

The helper enables `EVIA_E2E_FULL_STACK=1`; the two complete console browser cases run
only with this flag. Without it, those cases are explicitly skipped, which is not
evidence of a working pipeline. CI prepares the full dependencies and enables the
flag. Shared component and parser cases remain separate from real API cases.

The workflow creates a project and event in the UI, preserves input after a real
request failure, publishes a saved revision, waits for an actual pipeline probe,
activates explicitly, creates a source and sends synthetic data without management
cookies. It verifies actual metric, trend, journey and ordered-funnel queries,
empty results and environment switching. It never replaces successful API replies
with fabricated results. A second case verifies masked one-time server credentials,
acknowledgement before closing, committed rotation with a lost response and an
idempotent retry, and actual source disablement. Automatic trace, video and failure
screenshots are disabled for these credential-bearing cases; explicit screenshots
capture only safe business pages, with reduced-motion rendering. Other error traces
can contain synthetic credentials: keep them private. Set `EVIA_E2E_ARTIFACT_DIR` only to a private local output directory
for screenshots; the default uses Playwright's ignored output directory.

Login budgets remain enabled. Repeated debugging against the same account may
need a cooldown; do not weaken authentication limits to make tests pass. Stop
owned API/worker processes and the selected dependency containers after use.
These checks demonstrate functional behavior, not production capacity or HA.
