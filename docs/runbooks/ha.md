# High availability and graceful drain runbook

This runbook describes how Evia processes behave in a multi-replica
deployment and how a rolling replacement avoids dropping client requests. It
complements the [observability runbook](observability.md) and the [backup
runbook](backup-restore.md).

## Process roles and statelessness

Every Evia process is stateless with respect to its own disk:

* **serve** holds no local session or job state. Sessions, projects, leases
  and idempotency ledgers live in PostgreSQL; rate limits live in Redis. Any
  replica can therefore answer any authenticated request.
* **workers** (ingest/query/erasure/retention/export/backfill) claim work
  through PostgreSQL row leases (`SKIP LOCKED`) or Kafka consumer groups. Two
  replicas never process the same lease, and an abandoned lease is reclaimed
  after its timeout, so a crashed replica cannot stall a queue.
* Metrics registries are process-local and scraped per instance; losing one
  replica only removes its samples, never the monitoring pipeline.

Horizontal scaling consequently needs no shared filesystem or sticky
sessions; a load balancer may round-robin across every healthy serve replica.

## Probe semantics

The two HTTP probes answer different questions and must not be conflated:

| Probe | Meaning while healthy | Meaning during shutdown |
| --- | --- | --- |
| `GET /healthz` | 204, process is alive | stays 204 until exit |
| `GET /readyz` | 204, dependencies reachable and accepting traffic | 503 with `Retry-After: 5` immediately on SIGTERM |

`/readyz` fails 503 in two independent cases: the dependency probe cannot
reach PostgreSQL/Redis/ClickHouse, or the process has started draining. A
load balancer or orchestration readiness gate must route only to instances
returning 204; liveness must use `/healthz` so a draining instance is not
killed before it finishes in-flight work.

## Drain sequence on SIGTERM

`runHTTP` in `cmd/evia/runtime.go` implements an explicit ordering driven by
`observe.DrainGate`:

1. The process receives SIGTERM/SIGINT and the root context is cancelled.
2. The drain gate flips to draining: `/readyz` returns 503 immediately while
   `/healthz` keeps returning 204. No dependency call is made while draining,
   so readiness fails even if a downstream is already unreachable.
3. The process waits `EVIA_SHUTDOWN_DELAY` (default `5s`, bounded `0..30s`)
   so the load balancer can observe the failed probe and stop forwarding new
   connections.
4. `http.Server.Shutdown` stops accepting new connections and waits up to 10
   seconds for in-flight requests to complete.
5. The supervisor's 15-second overall deadline still applies; a request that
   cannot finish is closed by the hard deadline rather than hanging the rollout.

Setting `EVIA_SHUTDOWN_DELAY=0s` restores immediate shutdown for single-host
or non-load-balanced deployments; the gate still flips so readiness reflects
the terminating state.

## Recommended multi-replica topology

* Run at least two serve replicas behind an L4/L7 load balancer with active
  health checks against `/readyz` and a failure threshold shorter than the
  drain delay.
* Run each worker role with at least two replicas; the lease and consumer
  group protocols already serialize ownership, so no leader election is
  needed.
* PostgreSQL, Redis and ClickHouse are the stateful tier and require their
  own managed HA (streaming replication/failover, Redis Sentinel/Cluster,
  replicated ClickHouse Keeper). Evia treats them as external dependencies
  and surfaces their health through `/readyz`; it does not embed failover.
  Redis Sentinel discovery and PostgreSQL multi-host failover are supported
  directly by the clients, see the dedicated sections below.
* Keep `EVIA_SHUTDOWN_DELAY` greater than the load balancer's health-check
  interval plus propagation time. With 5-second checks the 5-second default
  is appropriate; faster checks can reduce it.
* Rolling updates must replace one replica at a time and wait for the new
  replica to report 204 on `/readyz` before draining the next.

## Redis Sentinel topology and failover semantics

The collection budget and the admission rejection counters both use Redis and
support two mutually exclusive connection modes, selected at startup:

* **Single endpoint (default).** `EVIA_REDIS_URL` points at one Redis or a
  managed virtual endpoint. Leaving Sentinel unset keeps this path
  byte-identical to previous releases.
* **Sentinel discovery.** `EVIA_REDIS_SENTINEL_ADDRS` (one to eight
  `host:port` seeds) and `EVIA_REDIS_SENTINEL_MASTER` replace the URL, which
  must then stay empty. `EVIA_REDIS_SENTINEL_TLS_ENABLED` defaults to true;
  plaintext is accepted only for loopback endpoints together with
  `EVIA_ALLOW_INSECURE_DEPENDENCIES=true`. Data-node and Sentinel passwords
  come from `EVIA_REDIS_PASSWORD` and `EVIA_REDIS_SENTINEL_PASSWORD`.

In Sentinel mode the process resolves the current master through the quorum
and follows promotions automatically, so a failover needs no restart or
configuration change. The safety invariants of the single-endpoint path are
preserved exactly:

* The budget limiter keeps `MaxRetries=-1`. While no master is available the
  admission call fails closed with `COLLECTION_BUDGET_UNAVAILABLE`; an
  ambiguous debit is never replayed, so token buckets cannot be charged twice.
* Admission stats keep `MaxRetries=0` and remain fail-open: a failover window
  only increments the dropped-write counter, never changes an admission
  decision.
* Both pools keep their bounded timeouts (at most five seconds), pool caps,
  RESP2 and five-minute connection lifetime. TLS verifies each dialed
  Sentinel or data endpoint against its own hostname because addresses are
  learned at runtime.

Production deployments must run at least three Sentinels across failure
domains with quorum two, enable `requirepass` and Sentinel authentication,
and use TLS on every endpoint. The local drill topology is plaintext and
loopback-only; it is never a production template.

The full failover sequence is reproducible with the standalone drill:

```
python3 tooling/ha/redis_failover_drill.py
```

It brings up one master, one replica and three Sentinels, stops the master,
waits for promotion, and proves fail-closed debits, same-process recovery and
exact Redis-side token accounting before tearing everything down. See
[verification/redis-sentinel-failover-2026-11-13.md](../verification/redis-sentinel-failover-2026-11-13.md).

## PostgreSQL multi-host failover semantics

Every Evia process opens PostgreSQL through one pgx connection pool
(`storage/postgres.Open`). The DSN accepts an optional comma-separated host
list, following the libpq multi-host convention:

```
postgres://evia:secret@pg-1:5432,pg-2:5432/evia?sslmode=verify-full&target_session_attrs=read-write
```

`config.ValidateDatabaseURL` enforces a closed contract before the pool is
constructed:

* one to eight `host:port` entries (ports default to 5432 when omitted);
* the only accepted query parameters are `sslmode` (the six libpq modes) and
  `target_session_attrs`, whose sole accepted value is `read-write` because
  every Evia runtime role writes; any other parameter fails startup;
* fragments, escaped paths and repeated parameters are rejected.

Single-host DSNs, including the existing `sslmode=disable` development URI,
keep their exact historical behavior; the validator adds no default session
attribute when `target_session_attrs` is absent.

With a multi-host DSN, pgx builds an ordered fallback list and applies the
read-write check to every connection attempt: a standby still in recovery is
refused and the next host is tried, so the pool can never silently execute
writes against a replica. When the primary stops, in-flight transactions
return their normal errors and `/readyz` fails (the pool `Ping` cannot reach
a read-write node); once a replica is promoted, new acquisitions connect to
it automatically, with no process restart or configuration change. Idle
connections are health-checked every minute and connections are recycled
after thirty minutes, so stale primary addresses leave the pool bounded.

The drill topology uses asynchronous streaming replication, which is enough
to prove the client-side failover contract. Production deployments must
choose their own replication durability (for example synchronous
replication with a bounded `synchronous_standby_names` quorum), provision
promotion through Patroni/repmgr or a managed control plane, enable TLS with
certificate verification, and place primaries and standbys across failure
domains. Evia does not perform promotion itself.

The full sequence is reproducible with:

```
python3 tooling/ha/postgres_failover_drill.py
```

It brings up a primary and a streaming standby, stops the primary, promotes
the standby with `pg_promote`, and proves bounded write failures, same-pool
recovery, an exactly contiguous committed sequence and an independent
read-back match before rejoining the old primary and tearing everything
down. See
[verification/postgres-streaming-failover-2026-11-13.md](../verification/postgres-streaming-failover-2026-11-13.md).

## Evidence

The drain ordering is covered by unit tests in
`internal/platform/observe/drain_test.go` (gate transitions, probe mapping)
and `cmd/evia/runtime_test.go` (readiness fails before the listener closes
while an in-flight request still completes; zero-delay path stays bounded).
A real process smoke test showed that after SIGTERM `/readyz` returns 503
within 16 milliseconds while `/healthz` stays 204 for the full drain window,
and the process exits 0 after the configured delay with its port released
and no error logs. See
[verification/graceful-drain-2026-11-12.md](../verification/graceful-drain-2026-11-12.md).

## Known limits

* The drain delay is process-local; a load balancer with slower endpoint
  propagation than the configured delay can still send a request that lands
  during listener shutdown. Tune the delay to the real propagation time.
* Long-polling or streaming requests longer than the 10-second HTTP shutdown
  budget are terminated by the supervisor deadline.
* Stateful-store failover itself is outside Evia's control and must be
  validated against the chosen managed service.
