#!/usr/bin/env python3
"""ClickHouse Keeper replication failover drill for Evia.

The script brings up the standalone two-node ReplicatedMergeTree topology
with a three-vote Keeper quorum from deploy/compose.ha-clickhouse.yaml, drives
the production pipeline sink through the TCP failover proxy, stops one node,
and proves four invariants on the live system:

  1. Writes fail while their routed node is unavailable instead of being
     silently accepted against a degraded single endpoint.
  2. The same long-lived sink resumes inserts through the proxy after the
     surviving node takes over, with no process restart.
  3. Keeper-coordinated replication converges both replicas to exactly the
     successful event set: identical counts, no gaps, no duplicates.
  4. After the stopped node returns, its replica re-attaches through Keeper
     and drains its replication queue without manual data repair.

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
COMPOSE_FILE = ROOT / "deploy" / "compose.ha-clickhouse.yaml"
PROJECT = "evia-ha-ch"
GO_ENV = {
    "EVIA_TEST_CH_HA_LB_URL": "http://127.0.0.1:18131",
    "EVIA_TEST_CH_HA_NODE1_URL": "http://127.0.0.1:18132",
    "EVIA_TEST_CH_HA_NODE2_URL": "http://127.0.0.1:18133",
    "EVIA_TEST_CH_HA_USERNAME": "evia",
    "EVIA_TEST_CH_HA_PASSWORD": "evia-ha-drill",
}


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


def ch_sql(service, sql):
    proc = compose("exec", "-T", service, "clickhouse-client", "-u", "evia",
                   "--password", "evia-ha-drill", "-q", sql)
    return proc.stdout.strip()


def wait_health(deadline_seconds=150):
    deadline = time.monotonic() + deadline_seconds
    while time.monotonic() < deadline:
        proc = compose("ps", "--format", "{{.Service}}|{{.Status}}", check=False)
        lines = [line for line in proc.stdout.splitlines() if line]
        if len(lines) == 4 and all("healthy" in line or line.startswith("keeper-3|") for line in lines):
            return
        time.sleep(2)
    raise DrillError("topology did not become healthy")


def wait_keeper_quorum(deadline_seconds=90):
    deadline = time.monotonic() + deadline_seconds
    while time.monotonic() < deadline:
        ok = True
        for service in ("ch-1", "ch-2"):
            proc = compose("exec", "-T", service, "clickhouse-client", "-u", "evia",
                           "--password", "evia-ha-drill", "-q",
                           "SELECT count() FROM system.zookeeper_connection", check=False)
            if proc.returncode != 0 or proc.stdout.strip() != "1":
                ok = False
        if ok:
            return
        time.sleep(2)
    raise DrillError("ClickHouse nodes never established their Keeper connection")


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


def evaluate(events):
    """Apply the failover invariants to one victim JSONL event stream."""
    summary = next((event for event in events if event.get("type") == "summary"), None)
    attempts = [event for event in events if event.get("type") != "summary"]
    if not attempts:
        raise DrillError("victim recorded no insert attempts")
    errors = sum(1 for event in attempts if event.get("result") == "error")
    if errors == 0:
        raise DrillError("no failure was observed while a ClickHouse node was down")
    if not any(event.get("phase") == "recovery" and event.get("result") == "ok" for event in attempts):
        raise DrillError("the same sink did not recover through the proxy")

    expected_seq, committed = 1, []
    for event in attempts:
        seq = int(event["seq"])
        if event["result"] == "ok":
            if seq != expected_seq:
                raise DrillError(f"successful sequence {seq} out of order, expected {expected_seq}")
            committed.append(seq)
            expected_seq += 1
        elif seq != expected_seq:
            raise DrillError(f"failed attempt changed sequence from {expected_seq} to {seq}")
    ok = len(committed)
    if ok == 0:
        raise DrillError("victim committed nothing before the failover")

    if summary is None:
        raise DrillError("victim log lacks the final convergence summary")
    for key, want in (("ok", ok), ("errors", errors), ("survivor", ok)):
        if int(summary[key]) != want:
            raise DrillError(f"summary field {key}={summary[key]} does not match expected {want}: {summary}")
    if not summary.get("database"):
        raise DrillError("summary must record the drill database for the post-reattach cross-check")
    return {
        "committed_inserts": ok,
        "failed_attempts": errors,
        "survivor_count": int(summary["survivor"]),
        "database": summary["database"],
    }


def run_baseline_test(env):
    proc = run([
        "go", "test", "-tags=integration", "-count=1",
        "-run", "TestReplicatedSinkBaseline", "./internal/pipeline",
    ], env=env, cwd=BACKEND)
    return proc.stdout.strip().splitlines()


def run_victim(duration_seconds, log_path, env):
    victim_env = dict(env)
    victim_env["EVIA_TEST_CH_HA_VICTIM"] = "1"
    victim_env["EVIA_TEST_CH_HA_VICTIM_LOG"] = log_path
    victim_env["EVIA_TEST_CH_HA_VICTIM_DURATION"] = f"{duration_seconds}s"
    return subprocess.Popen(
        ["go", "test", "-tags=integration", "-count=1",
         "-run", "TestReplicatedSinkFailoverVictim", "./internal/pipeline"],
        cwd=str(BACKEND), env=victim_env, text=True,
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
    )


def ch_sql_retry(service, sql, deadline_seconds=120):
    """Poll SQL through transient docker/containerd startup errors."""
    deadline = time.monotonic() + deadline_seconds
    while time.monotonic() < deadline:
        proc = compose("exec", "-T", service, "clickhouse-client", "-u", "evia",
                       "--password", "evia-ha-drill", "-q", sql, check=False)
        if proc.returncode == 0:
            return proc.stdout.strip()
        time.sleep(2)
    raise DrillError(f"SQL never succeeded on {service}: {sql}")


def wait_replica_reattach(deadline_seconds=120):
    deadline = time.monotonic() + deadline_seconds
    while time.monotonic() < deadline:
        proc = compose("exec", "-T", "ch-1", "clickhouse-client", "-u", "evia",
                       "--password", "evia-ha-drill", "-q",
                       "SELECT count() FROM system.replicas WHERE is_readonly=0 AND queue_size=0",
                       check=False)
        if proc.returncode == 0 and proc.stdout.strip() == "1":
            return
        time.sleep(2)
    raise DrillError("the restarted replica did not reattach and drain its queue")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--duration", type=int, default=45, help="victim loop duration in seconds")
    parser.add_argument("--keep", action="store_true", help="leave the topology running for inspection")
    args = parser.parse_args(argv)

    phases = []
    started = False
    topology_removed = False
    try:
        compose("up", "-d")
        started = True
        wait_health()
        wait_keeper_quorum()
        members = ch_sql("ch-1", "SELECT count() FROM system.clusters WHERE cluster='evia_ha_cluster'")
        if members != "2":
            raise DrillError(f"expected two cluster replicas, got {members}")
        phases.append({"phase": "keeper_quorum", "status": "ok", "cluster_members": members})

        env = dict(os.environ)
        env.update(GO_ENV)
        baseline = run_baseline_test(env)
        phases.append({"phase": "baseline_integration_test", "status": "ok", "output": baseline})

        with tempfile.TemporaryDirectory(prefix="evia-ch-drill-") as tmp:
            log_path = os.path.join(tmp, "victim.jsonl")
            victim = run_victim(args.duration, log_path, env)
            time.sleep(3)
            stop_started = time.monotonic()
            compose("stop", "ch-1")
            phases.append({"phase": "node_stop", "status": "ok"})

            victim_output = victim.communicate(timeout=args.duration + 120)[0]
            if victim.returncode != 0:
                raise DrillError(f"victim process failed:\n{victim_output}")
            verdict = evaluate(parse_event_log(Path(log_path).read_text()))
            outage_ms = int((time.monotonic() - stop_started) * 1000)
            phases.append({"phase": "victim_evaluation", "status": "ok",
                           "outage_ms": outage_ms, "verdict": verdict})

        compose("start", "ch-1")
        wait_replica_reattach()
        phases.append({"phase": "replica_reattach", "status": "ok"})

        # Two-node convergence on the victim's own table after Keeper-led
        # catch-up, then explicit cleanup since the victim test keeps the
        # database alive for this cross-check.
        database = verdict["database"]
        converge_deadline = time.monotonic() + 90
        replica_counts = {}
        while time.monotonic() < converge_deadline:
            replica_counts = {
                service: ch_sql_retry(service, f"SELECT count() FROM {database}.event_facts_raw")
                for service in ("ch-1", "ch-2")
            }
            if len(set(replica_counts.values())) == 1 and next(iter(replica_counts.values())) == str(verdict["committed_inserts"]):
                break
            time.sleep(2)
        expected = str(verdict["committed_inserts"])
        if any(value != expected for value in replica_counts.values()):
            raise DrillError(f"replicas diverged after reattach: {replica_counts}")
        phases.append({"phase": "two_node_convergence", "status": "ok", "counts": replica_counts})
        for service in ("ch-1", "ch-2"):
            ch_sql(service, f"DROP DATABASE {database} SYNC")

        # Final independent read through both direct node endpoints.
        counts = {service: ch_sql_retry(service, "SELECT count() FROM system.zookeeper_connection")
                  for service in ("ch-1", "ch-2")}
        if any(value != "1" for value in counts.values()):
            raise DrillError(f"Keeper connections after recovery mismatch: {counts}")
        phases.append({"phase": "post_recovery_keeper_links", "status": "ok", "connections": counts})
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
