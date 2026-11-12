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
* Keep `EVIA_SHUTDOWN_DELAY` greater than the load balancer's health-check
  interval plus propagation time. With 5-second checks the 5-second default
  is appropriate; faster checks can reduce it.
* Rolling updates must replace one replica at a time and wait for the new
  replica to report 204 on `/readyz` before draining the next.

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
