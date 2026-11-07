# Distributed collection admission budget

`backend/internal/platform/budget` implements the ingestion `Budget` port using Redis and go-redis/v9. It reserves the event count of one authenticated SDK batch atomically across service replicas. It does not use PostgreSQL counters, local credit leases or per-event network operations.

## Admission and failures

The collector resolves the project and environment from the authenticated source, then charges the bounded raw batch count before fetching plans or validating individual events. Invalid items and later queue failures still consume admission work; retries are charged again. This is a rate limit on admission attempts, not a count of unique persisted events.

Each project/environment has one hash key, with a validated namespace prefix and canonical lowercase project UUID. The environment is one of development, staging or production. A batch of 1–50 events either consumes all requested credits or none. Exhaustion returns `fault.Limited`, code `COLLECTION_BUDGET_EXCEEDED`, for HTTP 429. Connection, timeout, cancellation, malformed stored state and policy-disagreement errors return `fault.Unavailable`, code `COLLECTION_BUDGET_UNAVAILABLE`, for HTTP 503. The adapter never falls back to an unlimited allowance or a local counter. Safe messages exclude credentials and event bodies.

One Lua script uses Redis TIME, reads the bucket, replenishes it and conditionally subtracts the whole batch in the same atomic operation. Time is quantized to milliseconds and balances use integer micro-events. Configured refill rates are rounded down to 0.001 events/second precision. Arithmetic is bounded below the exact-integer limit of a Lua number; idle duration is clamped before multiplication. Fractional credits remain in the hash rather than being rounded into whole events on each call. Backward clock movement cannot create credits; a bounded backward offset also extends the expiry horizon.

The script uses EVALSHA with go-redis's NOSCRIPT fallback to EVAL. It is a single static script, with one key per invocation. This requires one Redis round trip for a warm SDK batch, plus the fallback round trip on a script-cache miss, regardless of event count. Automatic command retries are disabled: a lost response may have consumed credits, so the caller receives an unavailable error without an automatic debit replay or a refund.

## Configuration

Construct with `budget.New(config)` and close the limiter during service shutdown. Construction validates and snapshots server-owned configuration without connecting. `DefaultConfig(url)` returns explicit initial values:

| Setting        | Default             | Accepted bounds                                                   |
| -------------- | ------------------- | ----------------------------------------------------------------- |
| Default rate   | 1,000 events/second | Finite, 0.001–1,000,000, rounded down to 0.001 precision          |
| Default burst  | 2,000 events        | At least MaxBatchEvents and at most 1,000,000                     |
| MaxBatchEvents | 50                  | 1–50, matching the collection request contract                    |
| Timeout        | 200 ms              | Positive, at most 5 seconds                                       |
| PoolSize       | 32 per process      | 1–256 active connections                                          |
| KeyPrefix      | evia:collection     | 1–64 lowercase namespaced characters, without braces or wildcards |

`Overrides map[Scope]Policy` assigns a rate and burst to a specific authorized project/environment. Maps are copied at construction and never modified while reserving. These values come from server configuration, not a collection request. The full burst refill horizon must be at most 24 hours after rate quantization. A process must not mix separate deployments under the same prefix unintentionally.

Use a `rediss://` URL for every non-local endpoint. Certificate and hostname verification remain enabled, with TLS 1.2 or newer. Plain `redis://` requires `AllowInsecureLocal=true` and a localhost or loopback IP endpoint. URL query parameters and fragments are rejected so they cannot override pool, timeout or TLS policy. Keep credentials in private environment configuration; never commit usable URLs. The adapter bounds dial, pool wait, read, write and total operation time. It disables extra pipeline pools and maintenance connections, limits dial attempts, and opens no idle connections in advance.

Replicas must agree on a scope's policy. The hash stores the rate and capacity; a differing replica fails closed instead of resetting the balance or retroactively applying a different rate. For a policy change, quiesce the affected scope for its old full refill horizon, let the key expire, then resume all replicas with the new policy. Do not delete active buckets or switch prefixes while old replicas can still admit the same traffic. Live policy migration is a separate operation and is not implemented here.

## State lifetime and operating limits

Every successful or denied reservation sets expiry to the full refill horizon. An idle key can therefore disappear only once its full capacity would be available again. A bounded backward wall-clock offset adds at most another 24 hours to that expiry; corrupt or larger future timestamps fail closed. Rejected input never allocates a key. Cardinality is one small hash for each recently used, authenticated project/environment, with at most three environment keys per project in a namespace.

Deploy Redis with enough memory and a no-eviction policy. Memory exhaustion then rejects admission instead of evicting live quota balances. Global atomicity assumes the same authoritative Redis state and server clock are retained. A flush, data-loss restart, asynchronous replica failover or a forward clock jump can reset or advance allowances; this adapter does not claim a hard limit across loss of the quota store. Production persistence, failover behavior, clock discipline and recovery gating require explicit deployment validation. The disposable local integration container has persistence disabled and provides test evidence only.

This version uses a standard single-endpoint client and serializes each active scope's budget decisions on Redis. The key format is compatible with a single hash slot, but no cluster routing, application sharding, leased quota scheme or production throughput claim is implemented. Capacity must be measured with the actual batch distribution, replica count, network and Redis resources. Future optimizations must preserve global admission bounds and prove any unused lease capacity and expiry behavior.

## Verification

Unit tests cover invalid configuration, key-space boundaries, TLS and connection limits, override snapshots, conservative rate quantization, and invalid requests before dependency access. The explicit integration suite requires a real authenticated Redis endpoint and fails if EVIA_TEST_REDIS_URL is absent. It uses unique namespace prefixes and removes only its own keys, never FLUSHDB.

The suite verifies independent concurrent clients sharing one event budget, complete-batch admission, project/environment isolation, per-scope policy overrides, server-time refill within a short bounded window, key expiry, unavailable dependencies, cancellation, corrupt state, policy disagreement and backward-clock state.

Run with private test configuration loaded by the development helper:

```sh
./deploy/dev-env run /tmp/evia-pipeline.env -- /workspace/.tools/evia/go-env -C backend test -race -tags=integration ./internal/platform/budget -count=1
```

The local suite passed against an authenticated Redis 8.10.3 container with no eviction and persistence disabled. This is distributed admission correctness evidence, not production capacity or recovery evidence.
