# Analysis discovery and validation

The reporting foundation implements three endpoints under `P = /api/v1/projects/{project_id}`, accepting management sessions or scoped machine credentials. OpenAPI and the query JSON Schema remain authoritative. [Saved analyses](saved-analyses.md), [dashboards](dashboards.md) and [machine access](machine-access.md) are delivered as separate modules; independent [atomic and composite formula metrics](metrics.md) are likewise delivered as their own versioned resources.

| Endpoint | Implemented behavior |
| --- | --- |
| `GET P/analysis-capabilities` | Authorized discovery of query kinds, aggregations, field operations and static query budgets. No query parameters. |
| `GET P/analysis-catalog?environment=development` | Published event versions and their supported property fields in the requested environment. Optional `limit` and `cursor`. |
| `POST P/query-validations` | Validate a complete `QueryRequest` against current authorization and published definitions, returning a normalized request and dependency hashes without executing facts or creating a job. |

The runtime registers discovery even when no execution engine is configured. `execution_available` means an engine is configured; it does not probe ClickHouse health or confirm that workers are running. Query kinds describe implemented query grammar, not additional permission grants. The filter DSL's three `op` enums (logical, scalar comparison, key-existence) are self-documented in the JSON Schema with their type applicability and the scope/presence/validity guard semantics, so machine and Agent consumers can read negation and missing-value behavior directly from the contract without internal docs. The top-level query enums (kind, environment, subject, accuracy, interval) carry the same self-descriptions, including the fail-closed reserved `resolved_person` subject, the approximate-accuracy applicability and the Monday-start `week` bucket. The seven inline system-column `key` enums (group_by, metric field, funnel same-entity field and the shared field/segment-field/filter definitions) carry the same self-descriptions: they distinguish built-in system columns from event-scoped custom properties, name the subject identifiers and execution-context columns, mark `event_time` as the only datetime system field, and record the column-specific presence and empty-string guards; the segment-field enum omits `event_time` because segment labels must be scalar. Existing asynchronous execution routes are registered only when that engine is configured.

## Authorization and boundaries

All three operations require current project authority. Human callers use a management session and current membership; session POST validation also requires Origin/CSRF protection despite being free of persistent writes. Owners and editors may validate raw details and identity-bearing requests; viewers are restricted to aggregate requests without raw identity access. Machine callers need `queries.read`, their fixed project/environment, and `queries.raw.read` when the full request requires raw access. Human sessions and machine Bearer authentication cannot be mixed. Source write credentials do not authorize discovery or queries. Environment selects release membership and must match the machine grant; [machine access](machine-access.md) defines the complete policy.

The reporting service owns discovery policy and projection through a narrow store interface. The PostgreSQL adapter reads catalog facts and reuses the analysis field resolver in a transaction, keeping current actor authorization and project share locks until completion. Validation does not change catalog, query-job, outbox or audit tables. It is not a PostgreSQL `READ ONLY` transaction: its shared row locks coordinate current authorization and publication. There is no new definition store or database migration.

## Catalog projection

Each catalog item identifies one immutable `(event_key,schema_version)` with its definition hash, version creation time and published display name. Draft-only definitions are excluded. A version published in the requested environment remains visible when historical or inactive.

- `published` is true for returned versions because immutable release membership exists in that environment.
- `active` means the version belongs to the environment's currently active release.
- `ingestion_ready` requires that active release, its delivery readiness flag and a matching unexpired readiness proof. It is false for inactive versions, even if another historical release has proof. Readiness expiry does not change activation.

These fields do not prove that a source is enabled, that historical facts exist, or that an execution dependency is healthy. There is no `queryable` boolean.

Only supported top-level property keys are projected: string, boolean, integer, number and arrays of those scalar types. Nested objects, nullable unions, unconstrained arrays and unsupported property types are omitted. A string's date-time format remains a string; `datetime` is a system-field type. Catalog items contain property references, not a separate system-field list. Capabilities describe the supported field types, including system datetime.

Each field returns type, required status, operators, grouping and aggregate permissions, plus metadata from that immutable version's `field_metadata`. Scalars support equality, membership (including `not_in`) and presence operators; strings add prefix/suffix/contains plus their `not_prefix`/`not_suffix`/`not_contains` negations, numbers add ordered comparisons and numeric aggregation. Arrays support equality, contains/`not_contains` and presence tests, but cannot group or aggregate. Every negated operator retains the presence/validity guard of its positive counterpart and never matches absent or non-typed values. Returned `nullable` is false for this supported schema subset. `missing_semantics=absent_key` and `null_semantics=present_but_not_a_typed_value` preserve the existing distinction: explicit null is present but is not a valid typed scalar value. Metadata is descriptive; this foundation does not implement unit algebra, sensitivity permissions or field governance.

Pagination uses event-key then numeric-version order, a limit of 1–200, and `{environment,catalog_revision,items,next_cursor}`. The signed opaque cursor is scoped to operation, actor, project and environment; it cannot be reused across those boundaries. Each page is a current transaction view. The cursor does not freeze a multi-page snapshot or activation/readiness state. `catalog_revision` is the environment's publication metadata revision, or zero before publication; it does not advance for readiness expiry or every activation change.

## Validation and dependencies

Send the same complete request accepted by the query API, including environment, absolute half-open time range and timezone. Validation applies existing structural, static-budget, type, identity and published-field rules. It does not scan ClickHouse, estimate cardinality, simulate execution cost, reserve queue capacity or promise successful execution. Static budgets remain 31 days for trends, 7 days otherwise, 20 event selections, 3 group fields, 10 metrics, 500 result rows, and shared project limits of 4 running / 32 queued queries. Trend intervals advertise the closed set `minute`/`hour`/`day`/`week`, where `week` is a Monday-start bucket in the request timezone (variable-duration `month` is intentionally excluded). Retention queries advertise `retention_granularities` (`day`, `week`, `month`) and the retention budgets `retention_max_periods` (12) and `retention_max_window_seconds` (372 days, the conservative 31-day-per-period month bound; day/week still cap at 84 days); their time range cannot exceed periods multiplied by granularity. Execution can still fail its actual read, result or availability budgets.

A successful response contains `valid=true`, `execution_performed=false`, `execution_available`, `catalog_revision`, `normalized_request`, `query_hash`, `dependencies` and `dependency_hash`. Normalization applies analysis defaults, sorts event selections/keys and removes internal `resolved_type`; false and zero remain their original scalar values. It preserves ordered funnel steps and other semantic ordering. Query hashes include project scope and the canonical normalized request. Dependency hashes include the canonical manifest, not a fact-data snapshot or authorization grant.

| `dependencies.selection_mode` | Scope represented |
| --- | --- |
| `event_versions` | Exact fact selection with one published version per selected event. |
| `funnel_steps` | Exact event versions in the ordered funnel steps. |
| `event_keys` | All schema versions of each selected key; `unfixed_event_keys` names those keys. |
| `all_events` | No fixed event selection; project/environment facts remain unrestricted by event version. |

`dependencies.event_versions` records explicitly referenced immutable definitions and hashes, including property references. In `event_keys` or `all_events` mode those entries are not a complete fixed fact selection. A property reference never pins count/UV facts by itself. The saved-analysis publisher requires actual fixed selection rather than treating any dependency hash as proof of pinning.

Submit `normalized_request` through the normal asynchronous query API to execute. Submission reauthorizes and resolves definitions again; worker claim, result publication and result reads keep their existing authorization checks. Validation output is not a token, reservation, saved configuration or permission to bypass those checks. See [analysis semantics](analysis.md) and [foundation evidence](verification/reporting-foundation-2026-11-08.md).
