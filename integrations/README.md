# Public business integration assets

These three `activity-*` skills guide a coding agent working in a business repository through event planning, publication, SDK installation and actual ingestion verification. They are separate from platform engineering instructions. They never install or overwrite the business repository's `AGENTS.md`.

The API helper requires the machine-identity authentication contract in the target deployment. It fails closed if that endpoint or credential is unavailable. An existing source write credential cannot substitute for a machine credential. Backend availability and end-to-end evidence must be checked against the deployed version; these assets alone do not establish either.

## Distribution

From an Evia checkout, run:

```sh
python3 integrations/tooling/package_assets.py --output /tmp/evia-integration-skills.zip
python3 -m unittest discover -s integrations/tests -v
```

The archive includes only the explicitly listed public skills, helpers, schema, example and README. Install the skill folders using your coding agent's supported skill location; keep `tooling`, `schemas` and `examples` beside the `skills` folder so relative references resolve. Review file collisions before extraction. No internal instruction files are included.

## Helpers

Python 3.10+ and its standard library suffice. `activity_api.py` makes individual management requests; it is not an automated publication CLI. It discovers the machine identity before every request and checks the explicit project/environment. The backend remains authoritative for each operation's current permissions. There is no session-cookie fallback, redirect following, implicit retry, SQL access, or readiness setter.

Supply credentials through `EVIA_MACHINE_TOKEN` from your secret manager, or `--credential-file` pointing to an owned regular file with no group/other access. Never paste a token into command arguments, skill files, manifests, chat, browser configuration or source control. Use HTTPS; `--allow-loopback-http` permits only explicit local development on loopback. The helper respects configured urllib proxies and `NO_PROXY`; it does not load a cookie jar. It reads at most 1 MiB per request body and 2 MiB per response. Each HTTP call has a 20-second absolute deadline covering DNS, connection, headers and body reads. This uses POSIX signals and requires the main thread; unsupported runtimes and an already active alarm timer fail closed. The caller's signal handler and inactive timer are restored. Identity discovery and the requested operation are separate calls. There is no overall workflow deadline; callers must bound retries and polling.

```sh
python3 tooling/activity_api.py --endpoint https://events.example \
  --project 00000000-0000-4000-8000-000000000001 --environment development \
  --output .activity/identity.json
```

Create `.activity` as a private local directory and ignore it in the business repository before use. Responses go only to a **new mode-0600 file** with `--output`; stdout reports the HTTP status without a body. Responses can contain write keys, one-time server secrets or raw event data. Keep them private, read only the fields needed for the task, and never commit them. Existing output files are refused. Without `--output` the response is omitted. HTTP failures report only their numeric status; errors never print response bodies, URLs or exception content. Use existing contract/API documentation to interpret failures; do not enable credential/body logging.

To make a request, supply a documented project path with `--path`, `--method`, optional `--body-file`, quoted `--if-match '"3"'` and `--idempotency-key`. Only the implemented integration paths are allowed. The helper does not manufacture headers or request bodies, transform invalid drafts, authorize a different environment, or retry side effects. After an ambiguous response, preserve the exact body, scope, revision and idempotency key for the retry. Source/event creation lacks idempotency: reconcile current resources before retrying.

`activity_manifest.py desired.json` performs structural validation, not the full published property-schema policy. The authoritative backend must still validate every draft and example. The companion JSON Schema documents the desired format; the Python validator enforces its structural subset and additional path rules without external dependencies. `schema_version`, `release_revision`, readiness and publication status are server results and do not belong in this desired manifest.

For membership comparison, assemble a **local** snapshot from all `GET /analysis-catalog?environment=…` pages and `GET /release-activations?environment=…`:

```json
{"project_id":"00000000-0000-4000-8000-000000000001","environment":"development","active_release_revision":0,"items":[]}
```

This wrapper is not an API endpoint. Re-read activation after pagination; restart if its revision changed. Pages are current views, not frozen snapshots. Run `activity_manifest.py desired.json --active-snapshot active.json`. It rejects undeclared removals and unused removal declarations. Retained keys still need a definition comparison; a membership diff cannot establish schema compatibility or preserve another author's edits.

The SDK is workspace source, not a published registry package. Supply a reviewed local SDK archive built from the intended Evia revision. These integration skills do not contain the SDK archive, create a machine principal, or provision ingestion/query workers.

## Reusable metrics

Deployments migrated through `012` also expose independent atomic metrics. The helper allows `/metrics`, `/{metric_id}`, `/draft`, `/validations`, `/versions`, `/versions/{version}` and `/versions/{version}/queries` beneath the explicit project. Composite formula metrics (migrations `013`/`014`, including nested composite references from `033`) expose the same lifecycle under `/formula-metrics` with identical sub-paths; the helper allowlist mirrors it one-for-one and likewise excludes archive/unarchive governance actions. Request shapes remain defined by the deployed OpenAPI; this is a separate workflow from publishing event definitions.

Use `metrics.read` to inspect existing definitions and versions. With `metrics.write`, create a draft with `Idempotency-Key`, or update its definition using the exact quoted `If-Match`. Bind published event versions, the raw identity subject, aggregation, fixed filter and compatible unit. Validate the current draft revision and expected published version, inspect the returned diff, then publish the returned validation hash with `metrics.publish` and a new idempotency key. A draft update does not alter an existing metric version.

Run a specific version with `metrics.run` and `queries.run`. Supply an absolute half-open time range and IANA timezone; omit `interval` for a whole-window aggregate, or choose minute/hour/day/week for a trend, with week buckets starting on Monday. Runtime filters are combined with the definition's filter using AND. Poll the ordinary query endpoints with `queries.read`, using bounded retries. Identity fields in filters or groups additionally require `queries.raw.read`. Never sum daily UV to infer whole-window UV. Units are declarations with no automatic numeric conversion; formula metrics additionally provide an explicit, closed-set `convert` expression node for millisecond↔second conversion only. Formula metrics run through the parallel `/formula-metrics` paths with the same `metrics.*` permissions. Saved analyses and dashboard widgets can pin immutable atomic metric versions, so later drafts or published versions never implicitly change an existing analysis or board; pinning composite formula metrics into saved analyses or boards is not wired yet.

These four permissions are explicit new grants. Existing analysis permissions do not imply them, and old machine principals retain their immutable permission sets. Request an appropriately scoped principal from the owner instead of attempting to expand an existing credential.
