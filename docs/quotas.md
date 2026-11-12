# Managed collection quotas

`GET`/`PATCH /api/v1/projects/{project_id}/quotas` make the Redis admission budget manageable by project owners. The runtime enforcer remains the Lua token bucket in `internal/platform/budget`; this feature adds durable per-project/environment overrides and the optimistic-revision ledger around them.

## Policy model

Every project/environment scope has an effective policy of `rate_per_second` (events/second) and `burst` (whole-batch token capacity). The serve process owns a static default from `EVIA_COLLECTION_RATE`/`EVIA_COLLECTION_BURST`. A row in `collection_quotas` (migration 024) exists only after an owner publishes an override; absence means the default stays effective. Bounds are identical to the budget engine normalization: rate 0.001–1,000,000 finite, burst 50–1,000,000, and the full-refill horizon must not exceed 24 hours. A policy that cannot be normalized is rejected before it is persisted.

`GET` requires an `environment` query parameter and returns the effective values plus the persistence metadata: `source` is `default` or `project_override`, `revision` is 0 for an unconfigured scope, and timestamps are null until an override exists.

## Write path and runtime consistency

`PATCH` is owner-only human governance (machines are rejected) and requires `If-Match`, `Idempotency-Key` and CSRF headers, matching the retention policy conventions.

1. The PostgreSQL transaction re-checks owner membership, validates the optimistic revision and records the idempotency ledger and an `collection.quota_updated` audit entry.
2. After commit, the serve process publishes the new policy to the live limiter through an atomic immutable snapshot swap (`Limiter.ReplacePolicies`) and removes the existing Redis bucket for the scope (`ResetBucket`).
3. The next reservation initializes a fresh bucket from the new policy. Without the reset, the Lua script detects the stored capacity/rate disagreement and fails closed until the bucket expires.

The response `runtime_status` is `applied` when the bucket was removed, or `bucket_reset_pending` when Redis could not delete it; in the pending case admission for that scope may return 503 until the old bucket reaches its full-refill expiry. A failure to publish the in-memory policy returns 503 `QUOTA_RUNTIME_APPLY_FAILED` while the durable row remains committed.

### Idempotent replays do not republish runtime state

A replayed idempotency key returns the originally accepted snapshot from the ledger. The service reports this as a replay and the HTTP layer skips runtime publication, so retrying an old request can never regress the limiter behind a newer accepted write. Real full-stack evidence: after updating a scope to a new rate, replaying the original key left both PostgreSQL and the live limiter at the newer rate.

## Startup loading

On boot, when collection is enabled, the serve process loads every persisted override and publishes them to the limiter before accepting traffic. A load or normalization failure aborts startup rather than serving with missing quotas. The diagnostics endpoint (`docs/diagnostics.md`) reports the same effective snapshot through its budget section, so the persisted and runtime views can be cross-checked.

## Multi-replica semantics

The current supported topology is a single serve process, where PATCH applies inline and immediately. In a multi-replica deployment, only the handling replica swaps its limiter synchronously; other replicas keep the old in-memory policy until restart, and their reservations would fail closed on the Redis policy mismatch. Cross-replica propagation is therefore part of the explicit high-availability work package and must not be assumed from this API.

## Evidence and tests

- Budget engine: atomic swap and reset unit tests plus a real Redis integration test proving fail-closed mismatch and recovery after reset.
- Persistence: real PostgreSQL integration tests covering owner authority, revision conflicts, idempotent replay/conflict, audit entries and the system-wide loader.
- HTTP: default and override views, runtime status mapping, 503 on publish failure, 400/401 boundaries and absent-route behavior.
- Full stack: owner PATCH changes the live Redis bucket (`capacity`/`rate` fields match the new burst/rate, token debit and full-refill TTL verified directly in Redis), boot reload restores overrides, and stale revisions return `QUOTA_REVISION_CONFLICT`.
