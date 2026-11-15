# Saved analyses

Saved analyses persist named aggregate trend or ordered funnel configurations in one project and environment. A mutable draft becomes an immutable published version after validation; executing an explicit version creates a normal durable query job. Contracts, generated API types/client methods, domain policy, PostgreSQL persistence and HTTP routes are implemented. The console includes structured editing, publication review, version history and execution results. Multi-chart dashboards and scoped machine authentication are implemented. A saved analysis can alternatively pin an immutable [metric](metrics.md) version as its source; metric formulas, cross-metric parameters and exports remain planned. See the [feature coverage audit](feature-coverage.md) for the broader delivery boundary.

The authoritative saved-analysis schemas, including `SavedAnalysisTemplate` and `SavedAnalysisDefinition`, are in `contracts/http/openapi.json`; executable query contracts remain under `contracts/query/`. See [analysis semantics](analysis.md), [catalog discovery](reporting-discovery.md) and [verification evidence](verification/saved-analyses-2026-11-09.md). Real integration package times of PostgreSQL **40.548s** and ClickHouse analysis **7.365s** are test durations, not endpoint latency or capacity measurements.

## Authentication and routes

Set `P = /api/v1/projects/{project_id}`. Routes accept either a management session or explicitly scoped machine Bearer authentication, never both. Session POST/PATCH requests require the configured management Origin and `X-CSRF-Token`; validation also requires these headers even though it makes no persistent changes. JSON requests use `Content-Type: application/json`. Source write credentials grant no report access. [Machine access](machine-access.md) defines the independent `analyses.read`, `analyses.write`, `analyses.publish` and `analyses.run` grants; running also requires `queries.run`, and identity-bearing requests need `queries.raw.read`.

Current project members can read aggregate configurations, validate them and execute versions. Owner/editor roles can create, edit and publish. These human roles are unchanged. The HTTP boundary authorizes the project; PostgreSQL repeats current membership/account or exact machine credential checks inside each operation. Item routes derive the immutable environment from the stored resource and reject environment overrides.

| Method and path                                                  | Required input                                     | Result                                                         |
| ---------------------------------------------------------------- | -------------------------------------------------- | -------------------------------------------------------------- |
| `GET P/saved-analyses`                                           | `environment`; optional `limit`, `cursor`          | `{items,next_cursor}`; UUID order, default 20, maximum 100     |
| `POST P/saved-analyses`                                          | `{environment,definition}`; `Idempotency-Key`      | 201 draft with `ETag`                                          |
| `GET P/saved-analyses/{analysis_id}`                             | No query parameters                                | Resource summary                                               |
| `GET P/saved-analyses/{analysis_id}/draft`                       | No query parameters                                | Draft with `ETag`                                              |
| `PATCH P/saved-analyses/{analysis_id}/draft`                     | Full `{definition}`; `If-Match`                    | Updated draft and `ETag`                                       |
| `POST P/saved-analyses/{analysis_id}/validations`                | `{draft_revision,expected_published_version}`      | Validation, immutable dependencies, semantic diff and hashes   |
| `GET P/saved-analyses/{analysis_id}/versions`                    | Optional `limit`, `cursor`                         | Full immutable versions, numeric order, default 20, maximum 50 |
| `POST P/saved-analyses/{analysis_id}/versions`                   | Counters plus `validation_hash`; `Idempotency-Key` | 201 immutable version                                          |
| `GET P/saved-analyses/{analysis_id}/versions/{version}`          | Positive safe integer version                      | Immutable version                                              |
| `POST P/saved-analyses/{analysis_id}/versions/{version}/queries` | `{time_range,timezone}`; `Idempotency-Key`         | 202 existing `QueryJob`                                        |

Lists return a null `next_cursor` at the end. Cursors are signed and scoped to actor/project plus environment for resources, or analysis ID for version lists. They are not cross-page snapshots. Unknown or repeated query parameters, duplicate JSON keys, unknown JSON properties and trailing JSON values reject. IDs are UUID strings. Revisions/versions stay within JavaScript's safe integer range. Idempotency keys must be a single header containing 1–128 visible ASCII characters. Errors use the shared `{code,message,request_id,details?}` contract and `X-Request-ID` response header.

## Definition, drafts and charts

A definition contains `title`, optional `description` (normalized to an empty string), `query` and `visualization`. Titles are trimmed and limited to 1–128 characters; descriptions are limited to 2,000 characters. Both input and the final normalized definition, including defaults, must fit **128 KiB**.

The context-free query template permits only `kind`, `event_versions`, `filter`, `subject`, `group_by`, `metrics`, `interval`, `funnel`, `retention`, `segment`, `path`, `attribution` and `limit`. It rejects environment, absolute time, timezone, identity filters/revisions, `event_keys`, cursors and internal resolved field types. Structural validation requires no invented timestamps. Actual environment/timezone/time are supplied only when executing.

Trend templates require 1–20 explicitly selected event versions, with one version per event. Funnel templates use their 2–8 ordered steps as the pinned selection and reject `event_versions`. Retention templates use their two boundary steps (`start_event` and `return_event`) as the pinned selection and reject `event_versions`; they require an explicit `uid`/`anonymous` subject, a `day`/`week` granularity and 1–12 periods, and never accept trend-only knobs (`metrics`, `group_by`, `interval`, `funnel`). Segment templates pin their single segment event and require a scalar field owned by that event version. Path templates pin an optional start event: with one they carry a `path_steps` dependency, while a path without a start event reads every in-window event and therefore records an `all_events` manifest with no event-version dependency. Attribution templates pin the conversion plus every 1–8 touchpoint as `attribution_steps`. All three require an explicit subject, reject `event_versions` and the trend-only knobs, and keep the same fail-closed property-ownership checks. Every property reference must belong to that exact selection; same-entity funnel fields must also belong to their corresponding steps. Published membership is checked in the resource environment, including count/UV queries without property references. Historical published definitions do not need current ingestion readiness or activation. Cross-version compatibility mappings and unfixed fact selection are outside this saved-template contract.

Existing aggregates, scalar/array filters, exact/explicit approximate precision and raw UID/anonymous semantics remain unchanged. False and zero retain their types. Missing metrics normalize to exact count; missing limit normalizes to 200. Event-version selection is sorted as a set; funnel steps, metric order and other meaningful arrays retain their order.

`visualization` accepts only a `kind`, with no executable options:

| Kind     | Compatible query                                   |
| -------- | -------------------------------------------------- |
| `metric` | Trend, exactly one metric, no interval or grouping |
| `line`   | Trend with an interval                             |
| `bar`    | Trend with an interval or grouping                 |
| `table`  | Trend, funnel, retention, segment, path or attribution |
| `funnel` | Funnel                                             |

Creation starts `draft_revision=1` and `published_version=0` (no publication). Every accepted PATCH replaces the full definition and increments only the draft revision. `If-Match` must be one quoted current revision, such as `"1"`; missing/malformed headers return 400 `REVISION_REQUIRED`, and stale revisions return 409 `SAVED_ANALYSIS_REVISION_CONFLICT`. Resource summary `title`/`kind` describe the current draft. Historical versions retain their own immutable names/content.

## Metric references

Instead of an inline `query`, a definition may provide `metric_ref: {metric_id, version}` to pin an immutable [metric](metrics.md) version as its data source. On input the two sources are mutually exclusive: a request cannot carry both `metric_ref` and an inline `query`, and the reference must resolve to a published metric in the same project and environment. The first release renders the referenced metric as one whole-range `metric` card, so `visualization.kind` must be `metric`; adding trend intervals or grouping on top of a referenced metric is a future extension.

The write transaction resolves the reference before persisting anything: it requires `metrics.read`, loads the exact metric version (never a latest pointer), rejects cross-environment or unpublished references, and derives the executable trend query from the metric definition. The stored definition therefore always carries both the `metric_ref` provenance pin and its derived `query`, which keeps every existing read, publication-witness and execution path unchanged. No database migration is required because definitions remain bounded JSON documents.

The dependency manifest adds an optional `metric` pin (`metric_id`, `version`, `definition_hash`) alongside the metric's own event-version dependencies. Inline analyses never carry this pin. At execution the backend re-reads the pinned metric version in the same transaction and fails closed if it is missing, its environment differs or its digest no longer matches (`METRIC_PIN_CONFLICT`), and it re-checks `metrics.read`. Because the pin is an immutable version, republishing or editing the metric never implicitly changes an already published analysis; selecting a newer metric version requires an explicit new saved-analysis publication. Dashboards reference saved-analysis versions, so a metric-backed analysis can be placed on a dashboard without any dashboard schema change and with the same immutable guarantees.

A metric-referencing create body omits `query` and supplies only the pin:

```json
{
  "environment": "development",
  "definition": {
    "title": "Governed lesson metric",
    "metric_ref": {"metric_id": "11111111-1111-4111-8111-111111111111", "version": 1},
    "visualization": {"kind": "metric"}
  }
}
```

The console saved-analysis editor exposes a data-source switch between 内联查询 (inline query) and 指标引用 (metric reference).

The run view renders every saved kind through one bounded result table. Advanced kinds use kind-aware Chinese column headers (for example `subjects` reads 转化主体数 for attribution but 到达主体数 elsewhere, and `share` reads 归因占比 vs 群体占比) and value formatting aligned with the ad-hoc workspace: proportions render as percentages, attribution `credit` keeps four-decimal weights, and retention/path arrays join into readable 留存率 or 事件路径 strings instead of raw objects. Unknown columns still fall back to their server label or key, so a new server column never breaks the table. The explanatory note under the table is kind-aware: trend keeps the missing-bucket/UV wording, while funnel/retention/segment/path/attribution each show their own semantics (first-step conversion, period-zero baseline, unique grouping, depth exclusion, direct-source fallback) instead of trend-only text. In metric mode it hides the inline sections, locks the visualization to the metric card and renders a picker that lists published metrics and their immutable versions; the same write input is submitted, so the browser and API share the one-source contract and validation.

## Validate and publish

Read the draft, then validate its exact `draft_revision` against the current `expected_published_version` (zero before the first publication). Validation resolves current published field types and returns `valid=true`, `execution_performed=false`, normalized definition, dependencies, definition/dependency hashes, semantic diff and `validation_hash`. It performs no fact scan, creates no job/audit/publication and reserves no queue budget. A successful validation does not predict actual scan cost or execution success.

The manifest fixes project/environment, exact event versions and immutable catalog definition hashes; its mode is `event_versions` or `funnel_steps`, with no unfixed event keys. The first publication's diff adds the whole definition. Later diffs separate metadata, kind, version selection, filters, subject, metrics, grouping, interval, funnel, limit and visualization changes. Canonical hashes preserve ordered meaning; the acknowledgement binds the analysis ID/scope, draft/published counters, normalized definition, dependencies and diff. Clocks and unrelated catalog revision changes are excluded. A hash is not authorization.

Publish with the same counters and returned `validation_hash`. Inside one transaction the backend reauthorizes, checks idempotency, reloads/revalidates the draft and baseline, recomputes the hash, writes immutable version/dependency rows, advances the publication pointer, and writes audit/idempotency records. Any failure rolls everything back. Published content/dependencies reject UPDATE/DELETE at the database boundary and carry scoped catalog/release-membership foreign keys.

Publication allocates the next version and leaves the draft revision unchanged. A newly published catalog or saved-analysis version cannot silently alter an older version. Publishing the same content under a new key is a new explicit publication, subject to current counters; content hashes are not global deduplication keys.

## Example: create, validate, publish and execute

These examples require an upgraded API, an authorized signed-in session, and a real `page_opened@1` already published in the chosen project/environment. UUIDs and session values below are synthetic placeholders; substitute your own authorized scope. The cookie jar and CSRF token come from normal session login. The Origin must match the configured management origin.

```bash
EVIA_API='http://127.0.0.1:8080'
EVIA_ORIGIN='http://127.0.0.1:5173'
EVIA_PROJECT='22222222-2222-4222-8222-222222222222'
EVIA_COOKIE_JAR='/tmp/evia-example.cookies'
EVIA_CSRF_TOKEN='<csrf-token-from-session-login>'
EVIA_REPORTS="$EVIA_API/api/v1/projects/$EVIA_PROJECT/saved-analyses"

curl --fail-with-body -b "$EVIA_COOKIE_JAR" \
  -H "Origin: $EVIA_ORIGIN" -H "X-CSRF-Token: $EVIA_CSRF_TOKEN" \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: example-create-1' \
  --data-binary @- "$EVIA_REPORTS" <<'JSON'
{
  "environment": "development",
  "definition": {
    "title": "Daily page visits",
    "description": "Count of the selected published page event",
    "query": {
      "kind": "trend",
      "event_versions": [{"event_key": "page_opened", "schema_version": 1}],
      "metrics": [{"op": "count", "accuracy": "exact"}],
      "interval": "day",
      "limit": 200
    },
    "visualization": {"kind": "line"}
  }
}
JSON
```

Use the response's `analysis_id`, counters and validation hash. For a fresh draft they are revision 1 and published version 0:

```bash
EVIA_ANALYSIS='<analysis_id-from-create-response>'
curl --fail-with-body -b "$EVIA_COOKIE_JAR" \
  -H "Origin: $EVIA_ORIGIN" -H "X-CSRF-Token: $EVIA_CSRF_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"draft_revision":1,"expected_published_version":0}' \
  "$EVIA_REPORTS/$EVIA_ANALYSIS/validations"

EVIA_VALIDATION_HASH='<validation_hash-from-validation-response>'
curl --fail-with-body -b "$EVIA_COOKIE_JAR" \
  -H "Origin: $EVIA_ORIGIN" -H "X-CSRF-Token: $EVIA_CSRF_TOKEN" \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: example-publish-1' \
  --data "{\"draft_revision\":1,\"expected_published_version\":0,\"validation_hash\":\"$EVIA_VALIDATION_HASH\"}" \
  "$EVIA_REPORTS/$EVIA_ANALYSIS/versions"

curl --fail-with-body -b "$EVIA_COOKIE_JAR" \
  -H "Origin: $EVIA_ORIGIN" -H "X-CSRF-Token: $EVIA_CSRF_TOKEN" \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: example-query-1' \
  --data '{"time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-08T00:00:00Z"},"timezone":"Asia/Shanghai"}' \
  "$EVIA_REPORTS/$EVIA_ANALYSIS/versions/1/queries"
```

For editing, GET the draft and replace the full definition using PATCH with its returned `ETag` as `If-Match`; revalidate the new revision before publication. Do not reuse a previous acknowledgement after editing.

## Execution, retries and concurrent changes

Execution binds the explicit immutable version's environment and supplied real half-open time range/IANA timezone. It revalidates publication membership/types and enters the same durable query queue as direct analysis: project maximum **4 running / 32 queued**, trend window **31 days**, funnel window **7 days**, maximum **500 result rows**, and existing execution/read/result budgets. It does not create a separate dashboard runner. If execution is not configured, submission returns 503 `QUERY_STORAGE_UNAVAILABLE`; configuration reads remain available. Configured execution support does not prove dependency or worker health.

Poll the returned query ID through existing `GET P/queries/{query_id}`, read its authorized `result_url` after success, and cancel through `DELETE P/queries/{query_id}`. Existing worker leases and authorization checks apply at claim, completion and result read. Results expire after one hour; jobs expire after 24 hours. Coverage remains provisional, and per-query watermarks do not establish completeness or a multi-query snapshot.

Create/publication idempotency is actor/project/environment/operation scoped, with publication additionally bound to the analysis. Equal key/content replays the original response, even if the draft or latest publication subsequently changes. It does not return today's mutable draft. Different content returns 409 `IDEMPOTENCY_CONFLICT`. Current authorization is checked before replay.

Execution keys are actor/project/environment/analysis scoped and bind the exact version plus normalized executable request. Equal instants expressed using different RFC3339 offsets normalize to UTC; timezone remains part of the request. Within the **seven-day retry window**, equal retries return the original job's current state and create no duplicate; a different version/time/query payload conflicts. If the original job expires or is deleted, retry returns 409 `QUERY_REQUEST_EXPIRED`, even while the key marker survives. The marker does not extend result retention. After seven days the marker may be removed and the same key can create a new job; use a new key intentionally for each new execution. Expired job cleanup can clear its nullable reference without deleting immutable configuration or blocking cleanup.

On a lost create/publish/execute response, retry the identical request with the same key. On a PATCH response loss, reload the draft and compare current content/revision before saving again. On `SAVED_ANALYSIS_REVISION_CONFLICT`, reload/merge the draft; on `PUBLISHED_VERSION_CONFLICT`, reload the publication baseline; on `VALIDATION_HASH_CONFLICT`, validate again and review the returned diff. A failed publication leaves no partial version, pointer, audit or idempotency response. A failed queue admission does not consume a successful execution retry marker. Revoked membership cannot be bypassed by an old hash, cursor, key or result URL.

## Explicit database upgrade

Migration `007_saved_analyses.sql` adds resource/draft/version/dependency/idempotency storage and immutability/scope constraints. Definitions use PostgreSQL `json` to preserve their normalized byte limit without JSONB rendering expansion. Migration `008_saved_analysis_queries.sql` adds durable seven-day execution retry metadata and a scoped nullable query-job reference. No existing migration is rewritten.

For an installation currently at migration 006, build the matching new binary, load that installation's private configuration, and explicitly run `./bin/evia migrate` to apply 007 then 008 before starting the upgraded API/workers. `serve` verifies the ledger/checksums and never migrates automatically. Preserve the existing catalog and published releases; bootstrap is not part of an upgrade. The isolated migration test verifies upgrading existing 006 metadata, but this does not replace operational backup/recovery validation for a deployment. Follow the [development runbook](runbooks/development.md) and deployment procedures for the intended environment.
