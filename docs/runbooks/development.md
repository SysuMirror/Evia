# Run the current control module locally

This source setup runs the standalone Go control API and React console. It covers
sessions, project access and implemented project operations. Check PROGRESS.md
for the current module evidence. Contracts for collection and analysis describe
future work; this setup has no event pipeline, Kafka, Flink or ClickHouse.

Use Go 1.27.3 with its matching `gofmt`, Node 24.22.1, pnpm 11.25.0, Python
3.12 or 3.13, Docker Engine with Compose v2, GNU Make and Git. Go race tests also
need a C compiler and CGO enabled on a supported platform. The database image is
PostgreSQL 17.9. Install tools from their official distributions; verify their
versions before running checks. Project timezones accept IANA names such as
`Asia/Shanghai` or `UTC`; the Go binary embeds timezone data and database sessions
use UTC.

Run commands from the repository root in Bash. Dependencies need network access
and writable package caches, a Python environment, browser installation directory
and build output. In a restricted environment, a private temporary tool directory
avoids writing to a read-only home:

```bash
export EVIA_TOOL_DIR="$(mktemp -d)"
export GOPATH="$EVIA_TOOL_DIR/go"
export GOMODCACHE="$EVIA_TOOL_DIR/go-mod"
export GOCACHE="$EVIA_TOOL_DIR/go-build"
export PIP_CACHE_DIR="$EVIA_TOOL_DIR/pip"
export PNPM_HOME="$EVIA_TOOL_DIR/pnpm-home"
export PLAYWRIGHT_BROWSERS_PATH="$EVIA_TOOL_DIR/browsers"
export PATH="$EVIA_TOOL_DIR/npm/bin:$PATH"
npm install --global --prefix "$EVIA_TOOL_DIR/npm" pnpm@11.25.0
python3 -m venv .venv
. .venv/bin/activate
python -m pip install -r tooling/requirements.txt
pnpm install --frozen-lockfile --store-dir "$EVIA_TOOL_DIR/pnpm-store"
pnpm exec playwright install --with-deps chromium
```

The browser installer may need system package installation privileges on Linux.
Install those prerequisites through your environment's supported mechanism when
administrator privileges are unavailable. Preserve the cache environment in
terminals running checks; remove the temporary directory when it is no longer
needed. The Makefile accepts `GO`, `GOFMT`, `PYTHON` and `PNPM` overrides for
version-specific executable paths.

## Generate private local configuration

`.env.example` documents inputs; its placeholders cannot run the API. Generate
random local credentials into an ignored, mode-0600 `.env`. This command refuses
to replace an existing file:

```bash
python - <<'PY'
import os
import secrets

database_password = secrets.token_hex(24)
settings = {
    'EVIA_POSTGRES_PASSWORD': database_password,
    'EVIA_DATABASE_URL': f'postgres://evia:{database_password}@127.0.0.1:55432/evia?sslmode=disable',
    'EVIA_LISTEN': '127.0.0.1:8080',
    'EVIA_PUBLIC_ORIGIN': 'http://127.0.0.1:5173',
    'EVIA_ALLOW_INSECURE_HTTP': 'true',
    'EVIA_CURSOR_KEY': secrets.token_hex(32),
    'EVIA_BOOTSTRAP_USERNAME': 'admin',
    'EVIA_BOOTSTRAP_DISPLAY_NAME': 'Administrator',
    'EVIA_BOOTSTRAP_PASSWORD': secrets.token_hex(24),
}
descriptor = os.open('.env', os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(descriptor, 'w') as stream:
    for key, value in settings.items():
        stream.write(f'{key}={value}\n')
PY
set -a
. ./.env
set +a
docker compose --env-file .env -f deploy/compose.dev.yaml up -d --wait postgres
mkdir -p bin
go -C backend build -o "$PWD/bin/evia" ./cmd/evia
./bin/evia migrate
./bin/evia bootstrap
```

Only source your own generated environment file. Keep its password in your local
password manager or inspect it privately to sign in; do not paste it into chat,
logs or commits. Bootstrap requires an empty account store, a username, display
name and a password of 12–72 bytes; repeating bootstrap fails rather than
overwriting an account. Run migrations and bootstrap with one administrative
owner before starting the API. `serve` checks the migration ledger and checksums
and never applies migrations automatically.

In one terminal with the environment loaded:

```bash
unset EVIA_BOOTSTRAP_PASSWORD
./bin/evia serve
```

In another terminal from the repository root:

```bash
pnpm dev
```

Open [the console](http://127.0.0.1:5173). Vite proxies `/api` to the API on
127.0.0.1:8080. Use the exact configured origin: `localhost` and `127.0.0.1` are
different origins. `/healthz` reports process health; `/readyz` also checks the
database. The console uses an HttpOnly session cookie, a per-session CSRF token
and Origin verification for mutations. Secure cookies are the default; only
explicit `EVIA_ALLOW_INSECURE_HTTP=true` permits this local HTTP origin. Login
rate limits remain enabled.

## Verify the module

```bash
make generated-check
make check
```

`generated-check` regenerates the Python-produced Go types/schema bundle, applies
`gofmt`, regenerates the TypeScript API types and fails on a diff from the checked-in
files. Review and commit intentional generated changes together with contracts.
`check` checks naming, OpenAPI and JSON Schema syntax, shared positive/negative
fixtures using the generated schema bundle, Go formatting/vet/race tests, and
frontend formatting/typechecking/build. These checks do not substitute for real
database and browser verification.

Use a dedicated disposable PostgreSQL database for tests, separate from any
database containing work you need. With the development Compose database running,
create a test database once:

```bash
docker compose --env-file .env -f deploy/compose.dev.yaml exec -T postgres createdb -U evia evia_test
export EVIA_TEST_DATABASE_URL="postgres://evia:${EVIA_POSTGRES_PASSWORD}@127.0.0.1:55432/evia_test?sslmode=disable"
make integration-test
```

The PostgreSQL integration helper creates a unique schema per test and drops only
that schema during cleanup. Its role needs schema creation privileges. Missing
configuration or an unreachable database is a failure. Do not point tests at a
production database. Tests exercise persistence and concurrency; they do not
establish production capacity.

For browser verification, initialize that disposable database once and run a
separate API against it after stopping your own development API that occupies
port 8080:

```bash
export EVIA_DATABASE_URL="$EVIA_TEST_DATABASE_URL"
export EVIA_BOOTSTRAP_USERNAME=browser-admin
export EVIA_BOOTSTRAP_DISPLAY_NAME='Synthetic browser administrator'
export EVIA_BOOTSTRAP_PASSWORD="$(python -c 'import secrets; print(secrets.token_hex(24))')"
export EVIA_E2E_USERNAME="$EVIA_BOOTSTRAP_USERNAME"
export EVIA_E2E_PASSWORD="$EVIA_BOOTSTRAP_PASSWORD"
./bin/evia migrate
./bin/evia bootstrap
unset EVIA_BOOTSTRAP_PASSWORD
./bin/evia serve
```

In a second terminal, preserve the same `EVIA_E2E_USERNAME` and
`EVIA_E2E_PASSWORD` privately and run:

```bash
make browser-test
```

Playwright starts Vite automatically; reserve port 5173 or stop your own console
process first. The tests use Chromium, real session login, real database writes,
reload persistence, CSRF/Origin failures, keyboard interaction and narrow layouts.
They create synthetic projects. Avoid repeated rapid runs against the same
account because login rate limits apply. For a repeated run, use the existing
dedicated account credentials; bootstrap does not reset its password. Browser
failure reports and traces may contain synthetic credentials; keep them private.

CI performs the same checks sequentially, then migrates and bootstraps a fresh
ephemeral database and exercises the real API through Chromium. It requires every
dependency and does not silently skip database or browser checks. The CI workflow
definition is not evidence that a hosted run has passed.

To stop the local database while retaining data:

```bash
docker compose --env-file .env -f deploy/compose.dev.yaml stop postgres
```

The named volume persists across container replacement. Replacing the password in
`.env` does not change an already initialized database role password. Preserve the
volume and matching credentials; use a separate Compose project and port mapping
when you need another isolated local instance.
