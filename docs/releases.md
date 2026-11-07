# Verified release activation and public configuration

Building a release creates immutable metadata. It does not change the environment's default revision or prove that collection is available. Readiness, activation, Kafka acceptance and query visibility are separate states.

## Readiness evidence

The delivery worker validates the stored manifest's canonical hash, project/environment/revision, offline contract and local schemas before requesting a proof. Remote schema references are rejected. Validation has bounded concurrency, a time budget, single-flight work for identical manifests and an aggregate byte/rule cache budget. Immutable manifests are limited to 16 MiB at build time.

A proof request contains the authorized project, environment, release ID, revision, immutable manifest hash, a random nonce and a short expiry. It is published to a separate private readiness topic using the same durable Kafka producer policy as collection. A broker acknowledgement alone is insufficient.

The probe consumer verifies the actual raw fact table and exact read view, including the precise timestamp migration, and checks effective fact INSERT/SELECT grants. It then synchronously persists a receipt in ClickHouse's private `pipeline_readiness` table. The requester reads back the exact scope, hash, nonce and request timestamps before accepting the proof. A stopped consumer, missing fact capability or absent receipt fails closed. Probe records never enter business event facts. Receipts expire after one day.

Apply ClickHouse migrations 001, 002 and 003 explicitly before starting the current writer/probe workers. PostgreSQL migration 006 adds delivery leases, activation history, proof consumption and the monotonic activation guard. No service creates these schemas implicitly.

Readiness leases default to one minute and are bounded between 30 seconds and five minutes. A worker refreshes newly published eligible releases, the current default and previously activated releases still inside their compatibility window. Scans and concurrency are bounded; short durable job claims prevent failed oldest releases from starving other projects. A failed refresh never extends a lease. Management reads, collection and public configuration derive readiness from a matching manifest and unexpired database-clock lease, even if a stored operational boolean remains true.

The API needs probe publication and receipt read permissions. The probe consumer needs receipt writes and the real fact writer capability. Use separate credentials when deploying separate roles. The readiness topic, quarantine and receipt tables are private operational data, not public interfaces.

## Activation

`POST /api/v1/projects/{project_id}/release-activations` requires an authorized owner/editor session, CSRF validation and `Idempotency-Key`. Its body contains `environment`, `release_id` and `expected_active_revision`. The first activation expects zero.

The service obtains a fresh verified proof, then performs one transaction that rechecks membership, project/environment/release scope, manifest identity and expected default revision. It consumes the proof nonce once, records the readiness lease, changes the active revision, records idempotency and writes an audit entry. A failed audit or stale comparison rolls everything back. Same-key retries return the original activation result; different content with the same key conflicts.

The active revision only increases. To roll back behavior, build and activate a new release revision containing the intended earlier definition. This avoids revision ABA and preserves immutable publication history.

A former default remains eligible for a configurable compatibility window, normally 24 hours and bounded from one minute to 30 days. Only previously activated revisions qualify; merely published revisions do not grant collection access. Every eligible revision still needs a fresh readiness lease. The collector caches authority for at most two seconds and never beyond a known lease or compatibility deadline. Eligibility is checked before queue submission; an already submitted event may receive its durable acknowledgement after that deadline.

## Public configuration

`GET /v1/config?write_key=...` bootstraps without a current revision; `current_release_revision=0` is equivalent. A browser source requires an exact configured Origin and must omit cookies and bearer credentials. A server source requires its own bearer secret and must not use a browser Origin. Source status, credential expiry and scope are read from PostgreSQL on every configuration request.

The response is `evia.public-config.v1` with only project ID, environment, release revision and the source-kind-allowed event keys, versions and validation schemas. A source can receive an empty event list. The response excludes source settings, audit text, event examples, triggers, display metadata and nested schema annotations/defaults/examples. Business property names are preserved, including a property named `description`.

A client already using a previously activated revision receives the current default only when its rules and meaning remain compatible. Removed events, tighter constraints, changed types/fact semantics or changed field metadata conservatively retain the accepted old revision. An incompatible old revision whose window has expired returns a conflict, rather than silently relabeling its values. Never-activated revisions are rejected.

ETags identify the filtered public representation. Matching `If-None-Match` can return 304 only after current source authorization, revocation, lease and compatibility checks. It cannot revive a disabled source or expired credential. Browser preflight allows only GET and the configuration ETag header, never credentialed CORS.

## Runtime and verification boundary

Run the fact consumer, private probe consumer and delivery renewal worker under supervision. Cancel and join siblings if one fails; readiness expires when renewal stops. Activation requests fail while durable readback is unavailable. A newly built release may become ready before an explicit activation, but readiness alone does not change the default.

Unit/HTTP tests cover strict queries and headers, CSRF, source authorization, ETag revocation, allowlisted output, schema compilation, metadata compatibility, stale proofs and bounded validation. PostgreSQL tests cover authorization, concurrent compare-and-swap, scoped idempotency, one-use proofs, leases, compatibility expiry, fair scans and audit rollback. These persistence policy tests use an explicit probe port fixture; separate real Kafka/ClickHouse tests prove broker-only failure, actual consumer capability/readback, scope/hash binding and absence of business fact pollution. The integrated runtime HTTP smoke is a separate required verification step.

Kafka acceptance means durable queue acknowledgement under the configured broker policy. It does not mean the event is already queryable, that a business action succeeded, or that identity/session processing completed. Stateful Flink processing and production throughput, high availability and disaster-recovery capacity are not established by this module.
