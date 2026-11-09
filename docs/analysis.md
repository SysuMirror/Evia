# Analysis service

The implemented service accepts the typed requests in `contracts/query/query-request.schema.json`. It provides durable asynchronous jobs for event details, trends and aggregate metrics, raw-identity journeys, and ordered funnels. SQL text, database names, arbitrary functions, and client-supplied resolved field types are never request inputs. Property references must name an event, an immutable schema version published in the requested environment, and a field in that schema. Values are bound through ClickHouse HTTP parameters.

The management session determines the actor. PostgreSQL checks project membership when submitting, reading, canceling, and publishing results. Owners and editors can access individual event details and journeys; viewers can submit and read aggregate queries. Permission is checked again when retrieving an existing result and while a worker owns a lease. A source write credential grants no analysis access.

## Discovery and validation

Session-authenticated `GET analysis-capabilities`, `GET analysis-catalog?environment=...` and `POST query-validations` are implemented beneath the project API path. They discover the supported grammar and published typed property subset, or validate a complete request without scanning facts or creating a query job. `execution_available` reports configured execution support, not dependency health. Validation returns canonical request/dependency hashes; `event_keys` and unselected `all_events` remain unfixed across schema versions. A property dependency does not pin the fact selection. Execution always reauthorizes and resolves definitions again. Details, catalog state and cursor semantics are in [reporting discovery](reporting-discovery.md).

## Query semantics

- Time ranges are half-open `[from,to)`. Requests use RFC3339 timestamps; an IANA timezone controls explicit trend buckets. A trend without an interval aggregates the entire range. Current query policy accepts years 1970 through 2100.
- Event details, trends, and journeys can pin facts with `event_versions: [{"event_key":"forum_page_view","schema_version":1}]`. The nonempty selection allows at most 20 distinct events, with one immutable version per event; duplicate pairs and multiple versions of the same event reject until compatibility rules exist. Each selected version must belong to a release in the authorized project and requested environment, including count/UV queries without property references. Historical published versions remain queryable without current ingestion readiness or activation. Property references must belong to the pinned selection. The generated fact predicate matches exact event/version pairs before aggregation. `event_keys` and `event_versions` cannot appear together; existing `event_keys` queries continue to include all schema versions of each key. Funnels reject `event_versions` because their ordered steps already pin each event version.
- PV is `count` over the published event explicitly chosen to represent one page view. The console requires that selection and preserves the chosen PV meaning with the submitted result snapshot; an arbitrary interaction count is not automatically a page-view count. It does not create or assume an available `sys.page_view` runtime definition. UV is `unique` with an explicit `uid` or `anonymous` subject. UID UV excludes absent and empty UIDs. Anonymous UV counts raw anonymous identifiers. Daily UV values must not be added to obtain whole-range UV.
- Exact aggregation is the default. Approximate unique counts and approximate p95 must be requested explicitly; result metadata reports approximate accuracy when any requested metric uses it. Numeric aggregates omit missing, null, and wrongly typed values; a numeric aggregate without observations is null rather than an invented zero. Count and unique counts over no observations are zero.
- Properties remain scoped to their event and schema version. Comparisons do not coerce false into zero or missing into a default value. `is_present` includes an explicit null, while `is_missing` does not. Property grouping returns a corresponding `group_N_present` column to distinguish absent values from explicit nulls.
- A UID journey includes only facts with that original UID. It does not attach earlier anonymous events or another account sharing a device. Anonymous queries use the raw anonymous identifier, including authenticated facts that retain that identifier; this is a device identifier, not a resolved person.
- Ordered funnels permit other events between steps, count each subject at most once per reached step, and use one event for at most one step. Every reached step must fit within the window from a candidate first step. Later first-step candidates are retained. Optional entity constraints require matching scalar values across their specified steps. Equal timestamps advance only with the same source, nonempty session and tab, and increasing sequence. Otherwise, timestamps must increase.

The read view deduplicates by project, environment, source, and event ID before analysis predicates or aggregates. Identical IDs from different sources remain different facts. The winner order is receipt time, canonical content hash, effective occurred time and effective event time. This avoids depending on eventual background merges for exact counts.

Trend queries use the same tuple-valued winner selection with only the columns needed by the request. Migration 004 supplies compact hash bytes and effective timestamps; historical parts remain correct before explicit materialization, but their read cost is not yet reduced. A necessary UID or anonymous-ID equality can first locate candidate source/event pairs through migration 005's projections. Every raw version of those pairs still participates in winner selection, and the complete event, version, time, identity and property predicates remain outside it. Projection availability affects cost, not the definition of a matching fact.

Numeric property guards recognize ClickHouse JSON categories `Int64`, `UInt64` and `Double`. Decimal and exponent values participate in numeric aggregation and comparisons; strings and booleans are not scalar numbers. Properties continue to use the original document. A proposed per-property layout was deferred because the database's raw-extraction functions normalize number tokens; the [property read design](plans/property-read-layout.md) records the compatibility requirements.

## Timestamp deployment order

Apply the explicit ClickHouse migration chain before deploying the corresponding sink. `001_event_facts.sql` remains unchanged. `003_event_precision.sql` retains millisecond partition/sort columns and adds two nullable nanosecond payload columns. The view exposes their effective values under the existing `occurred_at` and `event_time` names as non-null `DateTime64(9)`.

Old stored millisecond values remain millisecond values. Lost historical precision is not reconstructed. New writes carry nanoseconds through the queue, sink, view, query comparison, and funnel ordering. Legacy-writer rollback remains compatible through defaults and view fallback. The new sink rejects missing precision columns instead of relying on a server setting that might silently skip unknown input fields. The migration is safe to reapply, checks unsupported legacy dates before DDL, and does not replace the raw table or its keys. The extra timestamp payloads and null maps add storage cost. Pipeline timestamps outside the supported 1900-01-01 through 2262-04-11T23:47:16.854775807Z range are rejected before queue publication or quarantined when decoding malformed queued records.

Current query readers additionally require `004_compact_winner_columns.sql` before deployment. Apply `005_identity_projections.sql` for selective identity access paths. Follow the [compact-column](runbooks/compact-winner-upgrade.md) and [identity-projection](runbooks/identity-query-upgrade.md) upgrade procedures for bounded historical materialization, disk reserves and rollback. These migrations add no implicit startup rewrite. The query role needs read access to the configured database's `event_facts_raw` as well as `event_facts`; clients still cannot submit raw SQL.

## Durable jobs and budgets

Jobs are queued in PostgreSQL, claimed with a generation-numbered lease, and published with a compare-and-swap check. An expired or canceled worker cannot publish a result. Execution leases last 45 seconds, queries have a 30-second execution deadline, and abandoned leases can be reclaimed within a three-attempt budget. Queue admission is serialized per project; at most four jobs execute and 32 wait per project. The configured number of worker loops supplies the process-wide limit. `Worker.Run` returns on storage errors, so its caller must supervise it with bounded restart backoff.

Cancellation changes durable job state and blocks later publication. Workers inspect their leases every 250 milliseconds. Cancellation also interrupts local funnel computation, closes the readonly HTTP query, and attempts to terminate that execution ID in ClickHouse. The ClickHouse execution deadline bounds remote work if a network failure prevents cancellation confirmation.

| Budget | Current limit |
| --- | --- |
| Trend time range | 31 days |
| Event, journey, or funnel time range | 7 days |
| Filter depth / total nodes | 8 / 100 |
| Event keys or pinned event versions / groups / metrics | 20 / 3 / 10 |
| Funnel steps / window | 2–8 / at most 7 days |
| Result rows | Request limit 1–500; default 200 |
| Published result JSON | 1.5 MiB, within the PostgreSQL 2 MiB storage guard |
| ClickHouse rows / bytes read | 2 million / 256 MiB |
| ClickHouse query memory / threads | 512 MiB / 2 |
| Funnel input | At most 100,000 rows and the 4 MiB response budget |
| Funnel candidates | 10,000 live states per subject; 1 million cumulative retained states |

Exceeding a result or execution budget returns an explicit failure instead of publishing a silently shortened aggregate or timeline. Queued jobs have a ten-minute wait deadline. Results expire after one hour; jobs expire after 24 hours, with bounded removal of expired records after a further hour. Reading an expired result cannot recover its stored event data.

## Evidence and remaining scope

Unit tests cover strict DSL rejection, pinned selection bounds and exact pair parameter binding, numeric precision declarations, Unicode UID limits, ordering, and cancellation of local computation. PostgreSQL integration tests cover admission races, lease generations, revocation, cancellation/completion races, result expiry, published field resolution, all selected versions' environment membership even for count/UV without properties, durable selection preservation, and rollback when completion audit fails. ClickHouse integration tests use isolated databases and actual migrations to verify mixed-version count/exact UV selection and existing cross-version event-key semantics, dedup before filters/counts, metric values, missing/null/false/zero/array behavior, identity boundaries, context links, funnel ordering, nanosecond upgrade compatibility, and disappearance of an actually running canceled query.

`data_watermark` is the latest received timestamp actually observed by that query, or null for no observations. It is not a completeness watermark. Coverage remains provisional, identity resolution is not applied, sample rate is one, and cache-hit metadata is false. No cache, settlement process, identity graph, or production capacity has been demonstrated by these tests.

Resolved-person queries and identity revisions reject explicitly until a real versioned identity graph exists. Request and result cursors also reject explicitly: this release provides bounded snapshots rather than timeline pagination. Retention/cohort queries, exports, and broader lifecycle operations are outside this implementation.

Run dependency tests with the repository-compatible Go toolchain and explicit test configuration; missing dependencies fail rather than silently substituting mocks:

```sh
./deploy/dev-env run /tmp/evia-pipeline.env -- \
  go -C backend test -race -tags=integration \
  ./internal/analysis ./internal/pipeline -count=1
```

PostgreSQL tests additionally require `EVIA_TEST_DATABASE_URL` and use isolated schemas, never global table truncation.
