# Dashboards

Dashboards combine explicitly published [saved-analysis versions](saved-analyses.md) in one project and environment. The backend provides mutable drafts, immutable publications, bounded layouts and real multi-widget runs through the existing query queue. Contracts, generated types, domain policy, PostgreSQL migration and HTTP routes are implemented. The console now provides editing, version publication/history and per-widget execution using these same APIs. Actual browser evidence is recorded in the [delivery verification](verification/dashboards-sdk-2026-11-09.md).

The authoritative schemas are in [OpenAPI](../contracts/http/openapi.json). Existing [analysis semantics](analysis.md), query budgets, provisional coverage and current project permissions continue to apply. Hard delete of dashboards, generic parameter bindings and cross-request result caching remain planned. Direct immutable metric-version widgets are implemented (migration `034`); bounded dashboard folders are implemented (migration `035`); scoped machine authentication is implemented.

## Authentication and scope

Set `P = /api/v1/projects/{project_id}`. Routes accept either a management session or explicitly scoped machine Bearer authentication, never both. Session POST, PATCH and DELETE requests require the configured management `Origin` and `X-CSRF-Token`; validation requires them despite making no persistent changes. Send JSON bodies with `Content-Type: application/json`. Source write credentials cannot authorize these operations.

Current project members can read configurations, validate drafts, submit runs and read aggregate query results. Owner/editor roles can create, save and publish dashboards. A run's creator or a current owner/editor can cancel it. Machines require the corresponding `dashboards.*` grant and can cancel only their own runs; execution also requires `queries.run` for every child. Identity-bearing widget requests additionally need `queries.raw.read`, including when inspecting a stored run after child-job cleanup. See [machine access](machine-access.md). Both the HTTP boundary and PostgreSQL check current authorization; old hashes, idempotency keys and result URLs do not preserve revoked access.

Creation fixes the dashboard's environment to `development`, `staging` or `production`. Item routes derive it from storage and reject client environment overrides. Published widgets must pin a source from the same project and environment: either a saved-analysis version or a published atomic metric version (`DASHBOARD_ANALYSIS_ENVIRONMENT_MISMATCH`/`DASHBOARD_METRIC_ENVIRONMENT_MISMATCH` otherwise). Saved analyses currently support aggregate trends and ordered funnels; metric widgets always expand to trend requests. Dashboards do not introduce raw event or journey widgets.

## Routes

| Method and path                                            | Input                                                      | Result                                                      |
| ---------------------------------------------------------- | ---------------------------------------------------------- | ----------------------------------------------------------- |
| `GET P/dashboards`                                         | Required `environment`; optional `view` (`active`/`archived`, default `active`), `limit`, `cursor` | `{items,next_cursor}`, UUID order; default 20, maximum 100  |
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
| `POST P/dashboards/{dashboard_id}/archive`                 | No body or query parameters; `dashboards.write`            | 200 resource with `archived_at`/`archived_by`; 409 if already archived |
| `POST P/dashboards/{dashboard_id}/unarchive`               | No body or query parameters; `dashboards.write`            | 200 active resource; 409 if not archived                    |
| `GET/POST P/dashboard-folders`                             | GET: required `environment`, optional `limit`/`cursor`; POST: `{environment,title,parent_id?}` + `Idempotency-Key` | GET 200 `{items,next_cursor}`; POST 201 folder with ETag; unknown parent 404, depth over 3 400 |
| `GET/PATCH/DELETE P/dashboard-folders/{folder_id}`         | PATCH `{title}` with quoted `If-Match`; DELETE has no body  | GET/PATCH 200 folder; DELETE 204 only when empty, else 409 `DASHBOARD_FOLDER_NOT_EMPTY`; stale revision 409 |

Lists end with `next_cursor=null`. Signed cursors bind the actor, project and list scope, including the `active`/`archived` view and any `folder_id` filter, so a cursor from one scope cannot page another; they are not cross-page snapshots. Unknown/repeated query parameters (an unknown `view` value returns `INVALID_ARCHIVE_VIEW`), unknown JSON properties, duplicate JSON keys and trailing JSON values reject. IDs are UUID strings; counters are bounded JavaScript-safe integers. Errors use the shared `{code,message,request_id,details?}` representation and `X-Request-ID` response header.

## Folder hierarchy

Dashboard folders are scoped organization metadata with no immutable witnesses. A folder belongs to one project/environment and is either a root node (`parent_id` omitted) or a child of another folder. The hierarchy depth is bounded to 3: creating a fourth-level node returns `DASHBOARD_FOLDER_TOO_DEEP`. A parent that does not exist in the same scope returns `DASHBOARD_FOLDER_PARENT_NOT_FOUND`; cross-environment parents get the same not-found answer so the API never probes foreign environments.

A folder's `parent_id` and `depth` are fixed at creation and protected by an immutability trigger; the only mutation is a rename through `PATCH` with the current quoted revision in `If-Match` (`REVISION_REQUIRED` when missing, `DASHBOARD_FOLDER_REVISION_CONFLICT` when stale), which bumps `revision` and returns the new ETag. Deletion is a hard delete of an **empty** folder only: any child folder or contained dashboard produces 409 `DASHBOARD_FOLDER_NOT_EMPTY`, and `ON DELETE RESTRICT` foreign keys are the database-level backstop. Reorganization therefore uses create folder, move dashboards, then delete the empty old folder.

Dashboards carry an optional `folder_id` on create and on every draft PATCH (full-replacement semantics: omitted or empty moves the dashboard back to the root). A missing or cross-scope folder fails with `DASHBOARD_FOLDER_NOT_FOUND` before insertion. The dashboard list accepts `folder_id=<uuid>` to list one folder or `folder_id=root` for unfiled dashboards; omitting the parameter applies no folder filter, preserving the previous flat listing behavior. Folders reuse `dashboards.read`/`dashboards.write` for sessions and the matching machine grants; no new permission is introduced.

### Console workflow

The console renders a folder sidebar next to the dashboard list: virtual nodes cover "all dashboards" (no filter) and "root-level" (`folder_id=root`), while stored folders appear indented by depth. Write members can create root/child folders (the parent selector only offers nodes below depth 3), rename with the current revision in `If-Match`, and delete empty folders through a confirmation modal; a non-empty delete surfaces the 409 `DASHBOARD_FOLDER_NOT_EMPTY` problem. The draft editor contains a folder selector whose assignment is part of the dirty state and is saved with the create or draft PATCH. An existing dashboard moves folders through a row action that first reads its current draft, then submits the same definition with the replacement `folder_id`, so the definition can never be overwritten by a stale client.

## Copy a dashboard

`POST P/dashboards/{dashboard_id}/copy` with an `Idempotency-Key` and a `DashboardCopy` body (`title`, optional `folder_id`) creates an independent dashboard in the same project/environment. The copy receives a fresh `dashboard_id` and fresh widget IDs, starts at `draft_revision=1` with no published version, and retains the source's pinned immutable references, layout, reading order, parameter declarations and per-widget bindings. Publication witnesses, runs, idempotency history and archive state are never carried over: the new dashboard is always active and unpublished. The optional target folder must exist in the source environment, otherwise the admission fails with `DASHBOARD_FOLDER_NOT_FOUND`; an unknown source returns `DASHBOARD_NOT_FOUND`.

The response is `201` with the new `DashboardDraft` and its quoted revision ETag. Same-key retries replay the identical copy; a changed body under the same key returns `IDEMPOTENCY_CONFLICT`. Copying requires `dashboards.write` and writes a `dashboard.copied` audit entry on the new resource. Copying an archived dashboard is allowed and yields a fresh active draft without changing the source's archive state.

### Cross-environment copy with explicit rebinding

When the body adds `target_environment` (a different closed-set environment) the copy is created in that environment instead of the source's. Because pinned analysis/metric versions are environment-scoped and cannot cross the boundary, every executable widget must then carry one entry in `rebindings`: `{widget_id, analysis_id, analysis_version}` for an analysis-sourced widget or `{widget_id, metric_id, metric_version}` for a metric-sourced one. The entry's kind must match the source widget (analysis stays analysis, metric stays metric), coverage must be exact—every executable widget exactly once—and presentation text cards need no entry because they carry over untouched. The store validates each target version structurally first (`INVALID_DASHBOARD_REBINDING` for missing/duplicate/kind/shape failures), then semantically: a missing version returns the existing `SAVED_ANALYSIS_VERSION_NOT_FOUND`/`METRIC_VERSION_NOT_FOUND`, and a version that exists in another environment returns `DASHBOARD_REBINDING_ENVIRONMENT_MISMATCH`. `target_environment` equal to the source environment returns `INVALID_ENVIRONMENT`; a same-environment copy must not send `rebindings`.

The cross-environment copy still starts at `draft_revision=1`, unpublished, with fresh dashboard/widget IDs and no carried-over runs, witnesses, archive or idempotency state. Parameter declarations and per-widget bindings are retained structurally; target-side publish validation re-runs field-existence checks against the rebound sources, so a binding whose field does not resolve in the target environment fails at publication with existing error codes rather than silently weakening semantics. The target `folder_id` must exist in the destination environment (`DASHBOARD_FOLDER_NOT_FOUND` otherwise). Idempotency is scoped per destination environment using operation `copy:{source_id}:{target_environment}`, so the same key may copy into different environments independently while same-key/same-env retries still replay and conflicts still return `IDEMPOTENCY_CONFLICT`. The console exposes a two-mode copy modal (same-environment vs. cross-environment with per-widget resource+version selectors), disables submit until every executable widget is rebound, and surfaces text-card carry-over explicitly.

## Define a draft

A definition contains `title`, optional `description` and 1–12 widgets. Titles are trimmed and limited to 1–128 characters; descriptions are limited to 2,000 characters. The normalized definition must fit 64 KiB of UTF-8 JSON.

Each widget has a stable UUID `widget_id`, title, `reading_order` and integer layout, and pins exactly one source kind: either the `analysis_id`/`analysis_version` pair of a saved-analysis version, a nested `metric_ref` (`metric_id` + `version`) pointing at a published metric version, or a presentation-only `text_card`. Providing none or several kinds is rejected by normalization and a database CHECK constraint. Keep its ID when moving, resizing or renaming it. Rendering configuration belongs to the referenced saved analysis or metric; dashboard definitions contain references and layout, without copied queries or results.

### Text-card widgets (migration `038`)

A text card carries Markdown prose instead of an executable source. Its `text_card` object has one required `content` string of 1–4,096 runes (the whole definition still fits the 64 KiB envelope). The Markdown subset is fail-closed at draft admission: raw HTML tags/comments, image syntax, `javascript:`/`vbscript:`/`data:` schemes and unclosed fenced code blocks are rejected, and inline links must use `http`/`https` targets. The subset supports headings, paragraphs, bold/italic, inline code, ordered/unordered lists, blockquotes, fenced code blocks and safe links. Enforcement is layered: the HTTP boundary walks every text card before service dispatch (`VALIDATION_FAILED`/`INVALID_DASHBOARD_TEXT_CARD`), normalization re-checks the same rules on every admission path, and the migration `038` SQL shape function provides structural defense in depth.

Text cards produce no dependency witness, no `dashboard_run_widgets` row and no child query. They cannot carry `parameter_bindings` (rejected by normalization and the SQL gate) and never enter the query queue or runtime filter widget list. A board containing only text cards is publishable with an empty `analyses` manifest (serialized as `[]`, never `null`), but run admission fails closed with `DASHBOARD_NO_EXECUTABLE_WIDGET`; the console disables the run action for such boards and renders text cards through a pure React subset renderer without `dangerouslySetInnerHTML`.

The layout has 12 columns. `x` is 0–11, `y` is 0–999, `width` is 1–12 and `height` is 1–24; `x+width` must be at most 12 and `y+height` at most 1,000. Rectangles cannot overlap. Reading order must be unique and contiguous from zero through the final widget; normalization returns widgets in that order. This gives a stable reading order for narrow layouts and later keyboard layout controls independently of visual coordinates.

A definition may declare at most 12 dashboard-level `parameters`. Each declaration has a stable `parameter_id` (1–64 characters), a 1–128 character `label`, a closed-set `type` (`string`, `number`, `boolean`), an `operator` valid for that type (string: `eq`/`neq`/`in`/`prefix`/`contains`; number: `eq`/`neq`/`in`/`gt`/`gte`/`lt`/`lte`; boolean: `eq`/`neq`), a `required` flag and an optional type-compatible `default`. Identifiers must be unique. A widget may carry at most 12 `parameter_bindings`, each mapping one declared `parameter_id` to a concrete field the widget's own pinned source owns (`system` fields, or `property` fields pinned to an event key and published schema version). Bindings never store a pseudo-global field name: the same display name never auto-binds, a parameter can bind at most once per widget, a field accepts at most one parameter, and a binding to an undeclared parameter is rejected by normalization and the migration `037` database CHECK before publication.

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

`POST .../{dashboard_id}/validations` returns `valid=true`, `execution_performed=false`, the normalized definition and a dependency manifest with each widget's pinned source (saved-analysis ID/version or metric ID/version) and definition/dependency/validation hashes. It checks the authorized scope and pinned versions without creating query jobs, reserving admission capacity or scanning event facts. Successful validation does not predict actual query cost or execution success.

### Dual-target dependency witnesses (migration `034`)

The relational witnesses keep generic identity columns and split into nullable dual-target foreign keys. `dashboard_dependencies` and `dashboard_run_widgets` each carry either the analysis pair (`analysis_id`/`analysis_version`, referencing `saved_analysis_versions`) or the metric pair (`metric_id`/`metric_version`, referencing `metric_versions`), with a soundness CHECK enforcing the exclusive choice. Composite foreign keys use MATCH SIMPLE, so pre-existing analysis-only rows (NULL metric columns) keep matching unchanged; the deferred publication trigger and the immutable `dashboard_definition_valid` shape function were updated to accept either source. No data backfill was required because existing rows already populate only the analysis pair.

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

Each executable predicate is the immutable source filter **AND** its targeted runtime filter. Runtime input cannot remove or replace fixed constraints. Existing field, value-type, selected-event-version, filter-depth and node budgets apply to the combined query. A property filter must belong to an event/version selected by that widget's pinned source — the saved analysis's query pins or the metric definition's `event_versions`; incompatible targets reject the entire submission. There is no generic declared parameter AST, implicit field-name alias or cross-event binding layer.

The backend stores and returns normalized runtime time, timezone and targeted filters on the run, preserving nanosecond bounds. Equivalent instants normalize to UTC; timezone remains part of the request. Published dashboard and saved-analysis definitions stay unchanged.

### Comparison periods

A run may carry an optional `comparison` spec (`previous_period`, `day`, `week`, `month` or `year`, with an `amount` of 1–12 for the calendar units). When present, every widget fans out a second child query over the same predicates, granularity and unit but a shifted window. Calendar shifts preserve local wall-clock time in the run timezone across DST transitions (they rebuild zoned wall-clock fields rather than subtracting fixed seconds), while `previous_period` mirrors the exact window duration. Migration `036` stores one `dashboard_run_widgets` row per `(widget, period)` with `period` in `current`/`comparison`; the run read model exposes the shifted job as `comparison` on each widget run and echoes the spec on the run. The comparison job is an independent work unit: its failure makes the overall run `partial`/`failed`, and cancelling the run cancels both windows. The comparison window doubles the child-query count (up to 24 jobs for 12 widgets) and stays subject to the same atomic admission and project queue budgets; runs without the spec keep the single-current-row behavior unchanged. The console run view offers a comparison selector (off, previous period, or 1–12 calendar units) and renders each widget's shifted-window result in a labeled "comparison window" pane beneath the current result, polling both jobs until the full run settles.

### Dashboard parameters

A run may carry an optional `parameter_values` object keyed by declared `parameter_id` (at most 12 entries). Each value must be a scalar or a homogeneous 1–20 element array matching the declaration type and operator bounds; strings are at most 1,024 characters and numbers stay within the safe-integer range. Unknown keys (`INVALID_PARAMETER`), type mismatches (`INVALID_PARAMETER_VALUE`) and oversize maps are rejected at admission before any child job is enqueued. A bound `required` parameter without a runtime value and without a default fails closed with `MISSING_PARAMETER_VALUE`; optional parameters with no value and no default compile to no predicate.

```json
{
  "time_range": {
    "from": "2026-11-01T00:00:00Z",
    "to": "2026-11-02T00:00:00Z"
  },
  "timezone": "UTC",
  "parameter_values": {
    "section": "forum",
    "min_lesson": 5
  }
}
```

Per widget, the server compiles each binding into an ordinary query filter AST — no new engine execution path exists — and merges predicates in the mandated order: the pinned metric/analysis fixed filter, then the parameter bindings, then the widget's local `widget_filters` entry. A parameter can never remove a fixed constraint. The compiled field still passes the existing field/type/event-version validation, so a stale binding to a field absent from the pinned source fails the run rather than executing. The run read model echoes `parameter_values`; changing any value changes the request hash, so same-key retries with different values return `IDEMPOTENCY_CONFLICT`. The console editor manages declarations and per-widget bindings, and the run view's parameter dialog collects typed runtime values with required-field and JSON-array hints.

### Console auto-refresh

The run view offers a client-side auto-refresh selector with two bounded intervals (60 seconds and 5 minutes) in addition to off. It reuses the same explicit-version run endpoint, so every refresh is a normal admitted run with a fresh idempotency key and remains subject to the project queue budgets — auto-refresh grants no extra concurrency. Each scheduled delay applies ±10% jitter (never below 60 seconds) so many open tabs cannot synchronize into a request burst. A tick never overlaps an in-flight run: while a run is queued, running or the controls are frozen, the scheduler retries every five seconds and enqueues only after the active run settles. Hidden tabs (`document.hidden`) pause scheduling and re-arm a fresh jittered interval when the tab becomes visible again; switching dashboard version resets the selector to off, and unmounting clears all timers. The control displays the estimated next-run time and a live countdown; turning refresh off or closing the view cancels pending ticks. No server-side scheduled or subscription execution exists.

Admission is atomic: all widgets enter the existing project query queue in one transaction, or no run, child jobs, audit entries or successful retry marker are committed. A two-widget run submitted when 31 of 32 queue positions are occupied fails as a whole. Dashboards share the existing project maximum **4 running / 32 queued** with direct and saved-analysis queries. They do not obtain an extra execution budget or reserve simultaneous starts.

The 202 response acknowledges admitted jobs. Workers execute the children independently under the existing type, authorization, lease and resource rules. Each query keeps the current 2,000,000-row read, 256 MiB read, 512 MiB memory and 30-second execution limits; result snapshots remain bounded to 500 rows and the existing response-size limits. Trend windows are at most 31 days; a dashboard containing a funnel must also satisfy that widget's 7-day query window. If execution is not configured, submission returns 503 `QUERY_STORAGE_UNAVAILABLE`; metadata reads remain available. Configuration does not prove worker or dependency health.

## Results, cancellation and expiry

Poll `GET P/dashboard-runs/{run_id}` using `poll_after_ms` while supplied. Each `widgets` item contains `widget_id`, its pinned reference (the analysis pair or `metric_ref`) and a nested existing `query` job with `query_id`, state, errors and, after success, `result_url`. Read each successful result through that existing authorized URL. Run/configuration responses do not copy result rows. A metric widget is expanded to a trend request through the same immutable `MetricTemplate` path as direct metric runs, so it consumes the same project query budget and never resolves an implicit latest version.

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

## Archive lifecycle

Migration `029_dashboard_archive.sql` adds the nullable `archived_at`/`archived_by` columns, the active-scope partial index and a marker-soundness check. `POST .../archive` sets `archived_at`/`archived_by` and moves the dashboard out of the default `GET P/dashboards` listing (filtered with a partial index on `archived_at IS NULL`). Pass `view=archived` to list only archived dashboards, each carrying its archive marker; `view=active` and an omitted parameter are equivalent. Archiving is reversible through `POST .../unarchive`, which clears both markers. Both actions require `dashboards.write`, take no body and are audited as `dashboard.archived`/`dashboard.unarchived`. Re-archiving an archived dashboard returns 409 `DASHBOARD_ALREADY_ARCHIVED`; restoring an active one returns 409 `DASHBOARD_NOT_ARCHIVED`.

Archived dashboards stay readable by ID, including drafts, immutable versions and run history. While archived, draft edits, validations, publications and new run enqueue all fail closed with 409 `DASHBOARD_ARCHIVED`; existing runs remain queryable and cancellable. The markers are optional nullable fields on the `Dashboard` response and are omitted for active dashboards.

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

Open a project and choose 自定义看板. Create a dashboard, then use the add-widget modal's 组件来源 switch to pin either a published saved analysis or a published atomic metric, each with an explicit immutable version; the editor rejects a widget carrying both or neither source before submission. Widget cards label the pinned source as `分析 vN` or `指标 vN`. Replacing a reference through 更换引用版本 swaps the whole source while keeping the widget ID, layout and reading order. Then edit widget positions, dimensions and reading order using labeled keyboard-operable controls. Saving a draft does not publish it; the pre-publication review lists every dependency witness with its source kind and pinned version, and requires acknowledgement before publication.

The published-version view binds a time range/timezone and optional typed field filters to explicit widgets. The runtime filter modal labels each target widget with its source kind and only offers fields from that widget's pinned event versions (analysis query pins or metric `event_versions`). Changing the selected version resets editable filters while preserving the identity of the earlier result snapshot. Analysis widgets keep their kind-specific charts and tables; metric widgets render the generic `QueryResult` columns with the shared type-aware cell renderer, plus accuracy/coverage badges, the bounded result table and metadata/raw-JSON details. Every card has its own state and cancel action. Unknown publication/run responses retain their original idempotency key and payload. Concurrent draft changes can be read and compared without discarding local input.

Desktop and mobile layouts use the shared dashboard grid. Charts fit the available card width; wide tables scroll within their own container. The console uses session authorization, not machine credentials.

The dashboard list has 活跃看板 / 已归档 views. In the active view, write-authorized members choose 归档 and confirm in a modal explaining that edits, publication and runs are blocked while archived; the row then leaves the list and a success notice is shown. The archived view lists archive time instead of update time, disables opening drafts and offers 恢复看板 to write-authorized members. View switching resets pagination, and lifecycle mutations invalidate both list views so the moved row appears immediately in its new stream.
