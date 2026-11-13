#!/usr/bin/env python3
"""Redis Cluster sharding failover drill for Evia.

The script brings up the standalone three-master/three-replica Redis Cluster
from deploy/compose.ha-redis-cluster.yaml, identifies the master owning the
budget bucket's hash-tag slot, stops that master mid-write, and proves four
invariants on the live system:

  1. Writes fail while their slot master is unavailable instead of being
     silently accepted against a degraded node (fail-closed budget).
  2. Redis Cluster promotes a replica and the same long-lived ClusterClient
     follows it through MOVED/ASK routing, with no process restart.
  3. Node-level retries stay disabled, so an ambiguous debit is never
     replayed: Redis-side token consumption stays inside an exact debit band.
  4. The stopped master rejoins as a replica and the cluster returns to
     three masters with all 16384 slots covered.

Only the Python standard library is used. Container control goes through the
docker CLI; all state is torn down with ``compose down -v``.
"""

import argparse
import json
import os
import subprocess
import sys
import tempfile
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
BACKEND = ROOT / "backend"
COMPOSE_FILE = ROOT / "deploy" / "compose.ha-redis-cluster.yaml"
PROJECT = "evia-ha-rc"
SEED_ADDRS = ",".join(f"127.0.0.1:{port}" for port in range(16479, 16485))
# The victim budget key is hash-tagged with this stable project/environment.
HASH_TAG = "{a3b09cf1-4ec3-4d54-a9c8-a18570deba40:production}"
NODE_SERVICES = [f"redis-node-{i}" for i in range(1, 7)]


class DrillError(RuntimeError):
    """A failover invariant could not be proven."""


def run(command, *, env=None, check=True, cwd=None):
    proc = subprocess.run(command, text=True, env=env, cwd=str(cwd) if cwd else None,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if check and proc.returncode != 0:
        raise DrillError(
            f"command failed ({proc.returncode}): {' '.join(command)}\n{proc.stderr.strip() or proc.stdout.strip()}"
        )
    return proc


def compose(*args, check=True):
    return run(["docker", "compose", "-f", str(COMPOSE_FILE), "-p", PROJECT, *args], check=check)


def redis_cli(service, *args, check=True):
    proc = compose("exec", "-T", service, "redis-cli", *args, check=check)
    return proc.stdout.strip()


def wait_project_gone(deadline_seconds=60):
    """Confirm every project container is removed before recreating them.

    A fast down/up cycle can otherwise let a delayed stop signal land on the
    freshly recreated container that reused the old name.
    """
    deadline = time.monotonic() + deadline_seconds
    while time.monotonic() < deadline:
        proc = run(["docker", "ps", "-a", "--filter", f"name={PROJECT}-",
                    "--format", "{{.Names}}"], check=False)
        if not proc.stdout.strip():
            return
        time.sleep(1)
    raise DrillError("old project containers never fully disappeared")


def wait_cluster_ready(service, deadline_seconds=180):
    deadline = time.monotonic() + deadline_seconds
    while time.monotonic() < deadline:
        state = redis_cli(service, "cluster", "info", check=False)
        if "cluster_state:ok" in state and "cluster_slots_ok:16384" in state:
            return
        time.sleep(2)
    raise DrillError(f"Redis Cluster never reached an ok state with full slot coverage (polled {service})")


def parse_slot_ranges(node_line):
    """Return the integer slot ranges announced by one CLUSTER NODES line."""
    ranges = []
    fields = node_line.split()
    for token in fields[8:]:
        if "[" in token:
            token = token.split("[", 1)[0]
        if "-" not in token:
            continue
        start, _, end = token.partition("-")
        if start.isdigit() and end.isdigit():
            ranges.append((int(start), int(end)))
    return ranges


def slot_owner_service(service, slot):
    """Map a slot to the compose service name of its current master."""
    nodes = redis_cli(service, "cluster", "nodes")
    service_by_ip = {f"172.31.80.{10 + i}": f"redis-node-{i + 1}" for i in range(6)}
    for line in nodes.splitlines():
        fields = line.split()
        if len(fields) < 9 or "master" not in fields[2]:
            continue
        host_port = fields[1].split("@", 1)[0]
        host = host_port.rsplit(":", 1)[0]
        if any(start <= slot <= end for start, end in parse_slot_ranges(line)):
            service = service_by_ip.get(host)
            if service is None:
                raise DrillError(f"slot owner {host} is not part of the drill topology")
            return service
    raise DrillError(f"no master found for slot {slot}")


def parse_event_log(text):
    events = []
    for line_number, line in enumerate(text.splitlines(), start=1):
        if not line.strip():
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError as exc:
            raise DrillError(f"victim log line {line_number} is not valid JSON: {exc}") from exc
        if not isinstance(event, dict) or "result" not in event and event.get("type") != "summary":
            raise DrillError(f"victim log line {line_number} has an unknown event shape")
        events.append(event)
    return events


def evaluate(events, duration_ms, refill_per_ms=20, event_cost=1_000_000):
    """Apply the failover invariants to one victim JSONL event stream."""
    summary = next((event for event in events if event.get("type") == "summary"), None)
    attempts = [event for event in events if event.get("type") != "summary"]
    if not attempts:
        raise DrillError("victim recorded no reserve attempts")
    errors = sum(1 for event in attempts if event.get("result") == "error")
    if errors == 0:
        raise DrillError("no failure was observed while the slot master was down")
    if not any(event.get("phase") == "recovery" and event.get("result") == "ok" for event in attempts):
        raise DrillError("the same ClusterClient did not recover after promotion")
    ok = sum(1 for event in attempts if event.get("result") == "ok")
    if ok == 0:
        raise DrillError("victim committed nothing before the failover")
    if summary is None or int(summary["ok"]) != ok or int(summary["errors"]) != errors:
        raise DrillError(f"summary counters do not match the event stream: {summary}")

    tokens = int(summary["tokens"])
    capacity = int(summary["capacity"])
    consumed = capacity - tokens
    max_refill = duration_ms * refill_per_ms
    lower, upper = ok * event_cost - max_refill, ok * event_cost
    if not lower <= consumed <= upper:
        raise DrillError(f"consumed units {consumed} outside the exact debit band [{lower}, {upper}]")
    return {
        "committed_reserves": ok,
        "failed_attempts": errors,
        "consumed_token_units": consumed,
        "exact_debit_band": [lower, upper],
        "bucket_key": summary["key"],
    }


def run_go_tests(args, env, run_expr):
    proc = run(["go", "test", "-tags=integration", "-count=1", "-run", run_expr, *args],
               env=env, cwd=BACKEND)
    return proc.stdout.strip().splitlines()


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--duration", type=int, default=45, help="victim loop duration in seconds")
    parser.add_argument("--keep", action="store_true", help="leave the topology running for inspection")
    args = parser.parse_args(argv)

    phases = []
    started = False
    topology_removed = False
    try:
        # Always start from a clean slate so a previous interrupted run cannot
        # leave a stopped master or stale cluster metadata behind.
        compose("down", "-v", check=False)
        wait_project_gone()
        compose("up", "-d")
        started = True
        wait_cluster_ready("redis-node-1")
        init_state = run(["docker", "inspect", "-f", "{{.State.ExitCode}}",
                          f"{PROJECT}-cluster-init-1"], check=False).stdout.strip()
        if init_state not in ("0", ""):
            raise DrillError(f"cluster init container exited with code {init_state}")
        masters = redis_cli("redis-node-1", "cluster", "nodes")
        master_count = sum(1 for line in masters.splitlines() if "master" in line.split()[2])
        if master_count != 3:
            raise DrillError(f"expected three masters, got {master_count}")
        phases.append({"phase": "cluster_ready", "status": "ok", "masters": master_count})

        env = dict(os.environ)
        env["EVIA_TEST_REDIS_CLUSTER_ADDRS"] = SEED_ADDRS
        baseline = run_go_tests(["./internal/platform/budget", "./internal/platform/admissionstats"], env,
                                "TestClusterBaseline")
        phases.append({"phase": "baseline_integration_tests", "status": "ok", "output": baseline})

        slot = int(redis_cli("redis-node-1", "cluster", "keyslot", HASH_TAG))
        owner = slot_owner_service("redis-node-1", slot)
        phases.append({"phase": "slot_ownership", "status": "ok", "slot": slot, "owner": owner})

        with tempfile.TemporaryDirectory(prefix="evia-rc-drill-") as tmp:
            log_path = os.path.join(tmp, "victim.jsonl")
            victim_env = dict(env)
            victim_env["EVIA_TEST_REDIS_CLUSTER_VICTIM"] = "1"
            victim_env["EVIA_TEST_REDIS_CLUSTER_VICTIM_LOG"] = log_path
            victim_env["EVIA_TEST_REDIS_CLUSTER_VICTIM_DURATION"] = f"{args.duration}s"
            victim = subprocess.Popen(
                ["go", "test", "-tags=integration", "-count=1",
                 "-run", "TestClusterVictimLoop", "./internal/platform/budget"],
                cwd=str(BACKEND), env=victim_env, text=True,
                stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            )
            time.sleep(3)
            compose("stop", owner)
            phases.append({"phase": "master_stop", "status": "ok", "service": owner})

            victim_output = victim.communicate(timeout=args.duration + 120)[0]
            if victim.returncode != 0:
                raise DrillError(f"victim process failed:\n{victim_output}")
            verdict = evaluate(parse_event_log(Path(log_path).read_text()), args.duration * 1000)
            phases.append({"phase": "victim_evaluation", "status": "ok", "verdict": verdict})

        # Cluster-driven failover: a replica must be promoted without
        # intervention. Poll a surviving node because the stopped master
        # cannot answer until it is explicitly restarted.
        wait_cluster_ready("redis-node-2")
        post_owner = slot_owner_service("redis-node-2", slot)
        if post_owner == owner:
            raise DrillError("slot ownership did not move to a promoted replica")
        phases.append({"phase": "automatic_failover", "status": "ok", "new_owner": post_owner})

        compose("start", owner)
        # The returning node must rejoin as a replica and restore three masters.
        rejoin_deadline = time.monotonic() + 90
        while time.monotonic() < rejoin_deadline:
            # node-1 may still be starting; tolerate transient exec failures.
            nodes = redis_cli("redis-node-2", "cluster", "nodes", check=False)
            if nodes.count("master") == 3 and "fail?" not in nodes and "handshake" not in nodes:
                break
            time.sleep(2)
        else:
            raise DrillError("the stopped node did not rejoin cleanly")
        wait_cluster_ready("redis-node-2")
        phases.append({"phase": "node_rejoin", "status": "ok"})
    finally:
        if started and not args.keep:
            compose("down", "-v")
            topology_removed = True

    report = {
        "status": "passed",
        "project": PROJECT,
        "duration_seconds": args.duration,
        "topology_removed": topology_removed,
        "phases": phases,
    }
    print(json.dumps(report, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except DrillError as exc:
        print(json.dumps({"status": "failed", "error": str(exc)}, indent=2), file=sys.stderr)
        sys.exit(1)
