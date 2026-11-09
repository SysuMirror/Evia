# Dashboards

Dashboards combine explicitly published [saved-analysis versions](saved-analyses.md) in one project and environment. The backend provides mutable drafts, immutable publications, bounded layouts and real multi-widget runs through the existing query queue. Contracts, generated types, domain policy, PostgreSQL migration and HTTP routes are implemented. The console now provides editing, version publication/history and per-widget execution using these same APIs. Actual browser evidence is recorded in the [delivery verification](verification/dashboards-sdk-2026-11-09.md).

The authoritative schemas are in [OpenAPI](../contracts/http/openapi.json). Existing [analysis semantics](analysis.md), query budgets, provisional coverage and current project permissions continue to apply. Dashboard folders, archive/delete operations, independently versioned metrics, generic parameter bindings and cross-request result caching remain planned. Scoped machine authentication is implemented.

## Authentication and scope

Set `P = /api/v1/projects/{project_id}`. Routes accept either a management session or explicitly scoped machine Bearer authentication, never both. Session POST, PATCH and DELETE requests require the configured management `Origin` and `X-CSRF-Token`; validation requires them despite making no persistent changes. Send JSON bodies with `Content-Type: application/json`. Source write credentials cannot authorize these operations.

Current project members can read configurations, validate drafts, submit runs and read aggregate query results. Owner/editor roles can create, save and publish dashboards. A run's creator or a current owner/editor can cancel it. Machines require the corresponding `dashboards.*` grant and can cancel only their own runs; execution also requires `queries.run` for every child. Identity-bearing widget requests additionally need `queries.raw.read`, including when inspecting a stored run after child-job cleanup. See [machine access](machine-access.md). Both the HTTP boundary and PostgreSQL check current authorization; old hashes, idempotency keys and result URLs do not preserve revoked access.

Creation fixes the dashboard's environment to `development`, `staging` or `production`. Item routes derive it from storage and reject client environment overrides. Published widgets must reference saved-analysis versions from the same project and environment. Saved analyses currently support aggregate trends and ordered funnels; dashboards do not introduce raw event or journey widgets.

## Routes

| Method and path                                            | Input                                                      | Result                                                      |
| ---------------------------------------------------------- | ---------------------------------------------------------- | ----------------------------------------------------------- |
| `GET P/dashboards`                                         | Required `environment`; optional `limit`, `cursor`         | `{items,next_cursor}`, UUID order; default 20, maximum 100  |
| `POST P/dashboards`                                        | `{environment,definition}`; `Idempotency-Key`              | 201 draft with `ETag`                                       |
| `GET P/dashboards/{dashboard_id}`                          | No query parameters                                        | Current resource summary                                    |
| `GET P/dashboards/{dashboard_id}/draft`                    | No query parameters                                        | Draft with `ETag`                                           |
| `PATCH P/dashboards/{dashboard_id}/draft`                  | Full `{definition}`; `If-Match`                            | Updated draft and `ETag`                                    |
| `POST P/dashboards/{dashboard_id}/validations`             | `{draft_revision,expected_published_version}`              | Normalized definition, pinned dependencies, diff and hashes |
| `GET P/dashboards/{dashboard_id}/versions`                 | Optional `limit`, `cursor`                                 | Immutable versions in numeric order; default 20, maximum 50 |
| `POST P/dashboards/{dashboard_id}/versions`                | Counters plus `validation_hash`; `Idempotency-Key`         | 201 immutable publication                                   |
| `GET P/dashboards/{dashboard_id}/versions/{version}`       | Explicit positive version                                  | Immutable publication                                       |
| `POST P/dashboards/{dashboard_id}/versions/{version}/runs` | `{time_range,timezone,widget_filters?}`; `Idempotency-Key` | 202 dashboard run with child query jobs                     |
| `GET P/dashboard-runs/{run_id}`                            | No query parameters                                        | Current run and per-widget states                           |
| `DELETE P/dashboard-runs/{run_id}`                         | CSRF headers for session callers                           | Cancels active children and returns current run             |

Lists end with `next_cursor=null`. Signed cursors bind the actor, project and list scope; they are not cross-page snapshots. Unknown/repeated query parameters, unknown JSON properties, duplicate JSON keys and trailing JSON values reject. IDs are UUID strings; counters are bounded JavaScript-safe integers. Errors use the shared `{code,message,request_id,details?}` representation and `X-Request-ID` response header.

## Define a draft

A definition contains `title`, optional `description` and 1–12 widgets. Titles are trimmed and limited to 1–128 characters; descriptions are limited to 2,000 characters. The normalized definition must fit 64 KiB of UTF-8 JSON.

Each widget has a stable UUID `widget_id`, explicit `analysis_id` and `analysis_version`, title, `reading_order` and integer layout. Keep its ID when moving, resizing or renaming it. Rendering configuration belongs to the referenced saved analysis; dashboard definitions contain references and layout, without copied queries or results.

The layout has 12 columns. `x` is 0–11, `y` is 0–999, `width` is 1–12 and `height` is 1–24; `x+width` must be at most 12 and `y+height` at most 1,000. Rectangles cannot overlap. Reading order must be unique and contiguous from zero through the final widget; normalization returns widgets in that order. This gives a stable reading order for narrow layouts and later keyboard layout controls independently of visual coordinates.

This create body uses synthetic IDs. First publish two real saved analyses in the chosen project/environment, then substitute their actual IDs and versions. For example, the first may count `page_opened@1` and the second count distinct raw UIDs for that same event. The event must already be published; creating a dashboard does not publish or activate events.

```json
{
  "environment": "development",
  "definition": {
    "title": "Page activity",
    "description": "Published page visits and readers",
    "widgets": [
      {
        "widget_id": "11111111-1111-4111-8111-111111111111",
        "analysis_id": "33333333-3333-4333-8333-333333333333",
        "analysis_version": 1,
        "title": "Page visits",
        "reading_order": 0,
        "layout": { "x": 0, "y": 0, "width": 6, "height": 4 }
      },
      {
        "widget_id": "22222222-2222-4222-8222-222222222222",
        "analysis_id": "44444444-4444-4444-8444-444444444444",
        "analysis_version": 2,
        "title": "Readers",
        "reading_order": 1,
        "layout": { "x": 6, "y": 0, "width": 6, "height": 4 }
      }
    ]
  }
}
```

Send it to `POST P/dashboards` with a fresh `Idempotency-Key`. Creation returns `dashboard_id`, `draft_revision=1`, `published_version=0` and `ETag: "1"`. Drafts may contain dependencies that fail subsequent validation; only a successful publication establishes immutable dependency witnesses.

For editing, read the draft and PATCH the complete replacement definition with its returned quoted ETag as `If-Match`. Saving increments the draft revision without changing an existing published version. Missing/malformed `If-Match` returns 400 `REVISION_REQUIRED`; a stale revision returns 409 `DASHBOARD_REVISION_CONFLICT`. After a lost PATCH response, reload and compare the draft before saving again.

## Validate and publish

Validate the exact draft revision against the current published pointer, using zero before the first publication:

```json
{ "draft_revision": 1, "expected_published_version": 0 }
```

`POST .../{dashboard_id}/validations` returns `valid=true`, `execution_performed=false`, the normalized definition and a dependency manifest with each widget's saved-analysis ID/version and definition/dependency/validation hashes. It checks the authorized scope and pinned versions without creating query jobs, reserving admission capacity or scanning event facts. Successful validation does not predict actual query cost or execution success.

The diff separates metadata, widget additions/removals, pinned analysis dependencies, layout and reading-order changes. The first publication adds the whole definition. Its `validation_hash` binds the dashboard scope, draft and publication counters, normalized definition, dependencies and diff. A hash is an acknowledgement of that content, not an authorization credential.

Review the returned diff and publish with the same counters and returned hash:

```json
{
  "draft_revision": 1,
  "expected_published_version": 0,
  "validation_hash": "<validation_hash-from-validation-response>"
}
```

Send this to `POST .../{dashboard_id}/versions` with a fresh `Idempotency-Key`. Publication reauthorizes and recomputes validation inside the transaction that writes the version, dependency witnesses, publication pointer, audit and idempotency response. Any failure rolls back the whole operation. Database constraints enforce same-scope dependency references and reject mutation/deletion of published versions and witnesses.

Publication increments the published version and leaves the draft revision unchanged. Later saved-analysis publications cannot change older dashboard versions. To upgrade a widget, explicitly edit its `analysis_version`, validate the new draft and publish another dashboard version. `PUBLISHED_VERSION_CONFLICT` requires reloading the baseline; `VALIDATION_HASH_CONFLICT` requires fresh validation and review.

## Run an explicit version

Submit to `POST .../{dashboard_id}/versions/{version}/runs`. Global absolute time and IANA timezone are required and bind to every widget:

```json
{
  "time_range": {
    "from": "2026-11-01T00:00:00.123456789Z",
    "to": "2026-11-02T00:00:00.123456789Z"
  },
  "timezone": "UTC",
  "widget_filters": [
    {
      "widget_id": "11111111-1111-4111-8111-111111111111",
      "filter": {
        "op": "eq",
        "field": { "kind": "system", "key": "uid" },
        "value": "synthetic-reader-one"
      }
    }
  ]
}
```

Omit `widget_filters` when no additional filters apply. Every supplied entry must identify a distinct widget in this exact dashboard version and contain an existing query filter AST; at most 12 entries are permitted. To apply a condition to several widgets, name each target explicitly. The example filters the first widget only.

Each executable predicate is the immutable saved-analysis filter **AND** its targeted runtime filter. Runtime input cannot remove or replace fixed constraints. Existing field, value-type, selected-event-version, filter-depth and node budgets apply to the combined query. A property filter must belong to an event/version selected by that widget's saved analysis; incompatible targets reject the entire submission. There is no generic declared parameter AST, implicit field-name alias or cross-event binding layer.

The backend stores and returns normalized runtime time, timezone and targeted filters on the run, preserving nanosecond bounds. Equivalent instants normalize to UTC; timezone remains part of the request. Published dashboard and saved-analysis definitions stay unchanged.

Admission is atomic: all widgets enter the existing project query queue in one transaction, or no run, child jobs, audit entries or successful retry marker are committed. A two-widget run submitted when 31 of 32 queue positions are occupied fails as a whole. Dashboards share the existing project maximum **4 running / 32 queued** with direct and saved-analysis queries. They do not obtain an extra execution budget or reserve simultaneous starts.

The 202 response acknowledges admitted jobs. Workers execute the children independently under the existing type, authorization, lease and resource rules. Each query keeps the current 2,000,000-row read, 256 MiB read, 512 MiB memory and 30-second execution limits; result snapshots remain bounded to 500 rows and the existing response-size limits. Trend windows are at most 31 days; a dashboard containing a funnel must also satisfy that widget's 7-day query window. If execution is not configured, submission returns 503 `QUERY_STORAGE_UNAVAILABLE`; metadata reads remain available. Configuration does not prove worker or dependency health.

## Results, cancellation and expiry

Poll `GET P/dashboard-runs/{run_id}` using `poll_after_ms` while supplied. Each `widgets` item contains `widget_id`, its pinned analysis reference and a nested existing `query` job with `query_id`, state, errors and, after success, `result_url`. Read each successful result through that existing authorized URL. Run/configuration responses do not copy result rows.

Admission is one transaction; results do not share an atomic data snapshot. One widget may finish or fail while others continue. Coverage remains provisional, and separate watermarks do not establish complete or identical data arrival across cards. Preserve the individual result metadata and errors.

| Run state   | Meaning                                                                                 |
| ----------- | --------------------------------------------------------------------------------------- |
| `queued`    | All children remain queued                                                              |
| `running`   | At least one child is queued/running and the run is no longer entirely queued           |
| `succeeded` | Every child succeeded                                                                   |
| `partial`   | All children are terminal, with both successful and unsuccessful widgets                |
| `canceled`  | Every child is canceled                                                                 |
| `expired`   | Every child is expired, including cleaned-up query references                           |
| `failed`    | No child succeeded and terminal outcomes do not meet the all-canceled/all-expired cases |

DELETE on the run cancels all currently queued/running children in one transaction. It preserves run history and already finished outcomes/results. Existing worker cancellation and lease behavior apply to remote execution. Cancellation can therefore leave a terminal `partial` run if some widgets already succeeded. An unauthorized cancellation returns `DASHBOARD_RUN_CANCEL_FORBIDDEN`.

Queued jobs have the existing ten-minute admission deadline; jobs expire after 24 hours, and successful results expire after one hour. A read applies the normal child expiry rules. The run's `expires_at` follows the earliest stored child job/result expiry observed during reads and can become earlier as children finish; queued deadlines are checked separately. Expired/deleted child jobs remain visible as expired widget references without retaining result data. Other successful, unexpired widgets remain independently readable.

## Idempotency and retries

Create, publication and run requests require one `Idempotency-Key` header containing 1–128 visible ASCII characters. Keys bind the current actor, project, environment, operation and normalized content; publication/run operations also identify the dashboard. Current authorization is checked before replay.

An identical create or publication retry returns its original response, even after subsequent draft/publication changes. Different content under the same key returns 409 `IDEMPOTENCY_CONFLICT`. Publication keys bind the expected counters and validation hash.

Run keys bind the explicit dashboard version, normalized absolute time, timezone and targeted filters. An identical retry returns the original run's current child states and never fans out again. Changed filters, version or runtime context conflict. A failed or canceled run replay does not start new work. If any original child is expired or has been cleaned up, retry returns 409 `DASHBOARD_RUN_REQUEST_EXPIRED`; use a new key for a new run. Dashboard retry markers currently have no key-recycling window, and they never extend result retention. Use a fresh key for each intentional refresh.

## Explicit migration and startup

Migration `009_dashboards.sql` adds dashboard drafts, immutable publications/dependency witnesses, idempotency records and run-to-query references. Exact normalized execution input is stored as JSON so PostgreSQL timestamp projections cannot discard sub-microsecond bounds. Query jobs continue to own results and their retention.

For an existing installation at migration 008, build the matching binary, load that installation's private configuration, then explicitly migrate before starting the upgraded processes:

```bash
mkdir -p bin
go -C backend build -o "$PWD/bin/evia" ./cmd/evia
# Load the intended installation's private configuration in this shell.
./bin/evia migrate
```

Restart the API and query workers with that same configuration and binary. For separate local terminals, the existing commands remain `./bin/evia serve` and `./bin/evia query-worker`. `serve` verifies the ledger/checksums and does not migrate automatically. Bootstrap is for a new account store, not an upgrade step. Dashboards add no separate worker process or ClickHouse migration; fact/query prerequisites still follow the deployment's existing pipeline setup.

Use the [development runbook](runbooks/development.md) for configuration and PostgreSQL startup, and the [complete local runtime](runbooks/console-verification.md) for ClickHouse and query workers. Apply upgrades through the intended installation's operational backup/recovery procedure; real integration tests do not establish production recovery or capacity.

## Console workflow

Open a project and choose 自定义看板. Create a dashboard, add published saved analyses with explicit versions, then edit widget positions, dimensions and reading order using labeled keyboard-operable controls. Saving a draft does not publish it; review its validated dependency/difference summary and acknowledge before publication.

The published-version view binds a time range/timezone and optional typed field filters to explicit widgets. Changing the selected version resets editable filters while preserving the identity of the earlier result snapshot. Each result has its own state, chart, complete table and coverage/accuracy details. Unknown publication/run responses retain their original idempotency key and payload. Concurrent draft changes can be read and compared without discarding local input.

Desktop and mobile layouts use the shared dashboard grid. Charts fit the available card width; wide tables scroll within their own container. The console uses session authorization, not machine credentials.
