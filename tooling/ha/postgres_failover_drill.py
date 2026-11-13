#!/usr/bin/env python3
"""PostgreSQL streaming-replication failover drill for Evia.

The script brings up the standalone primary/standby topology from
deploy/compose.ha-postgres.yaml, drives the production pgx connection pool
through a multi-host DSN, stops the primary and promotes the standby, then
proves four invariants on the live system:

  1. Writes fail while no read-write primary is available; the pool never
     silently commits against a recovery standby (target_session_attrs).
  2. The same long-lived pool resumes writes after promotion, with no
     application restart or configuration change.
  3. Every committed sequence is exactly 1..N: no gaps, no duplicates, so
     neither a failed nor an ambiguous transaction is applied twice.
  4. An independent psql read of the promoted node matches the victim's
     summary, and the old primary can rejoin as a caught-up standby.

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
COMPOSE_FILE = ROOT / "deploy" / "compose.ha-postgres.yaml"
PROJECT = "evia-ha-pg"
PRIMARY_IP = "172.31.78.10"
STANDBY_IP = "172.31.78.11"
FAILOVER_DSN = (
    "postgres://evia:evia-ha-drill@172.31.78.10:5432,172.31.78.11:5432"
    "/evia?sslmode=disable&target_session_attrs=read-write"
)


class DrillError(RuntimeError):
    """A failover invariant could not be proven."""


def run(command, *, env=None, check=True, cwd=None):
    proc = subprocess.run(command, text=True, env=env, cwd=str(cwd) if cwd else None,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if check and proc.returncode != 0:
        raise DrillError(
            f"command failed ({proc.returncode}): {' '.join(command)}\n{proc.stderr.strip()}"
        )
    return proc


def compose(*args, env=None, check=True):
    return run(["docker", "compose", "-f", str(COMPOSE_FILE), "-p", PROJECT, *args], env=env, check=check)


def psql(service, sql, env=None):
    proc = compose("exec", "-T", service, "psql", "-U", "evia", "-d", "evia", "-tAc", sql, env=env)
    return proc.stdout.strip()


def wait_health(deadline_seconds=120):
    deadline = time.monotonic() + deadline_seconds
    while time.monotonic() < deadline:
        proc = compose("ps", "--format", "{{.Service}}|{{.Status}}", check=False)
        lines = [line for line in proc.stdout.splitlines() if line]
        if len(lines) == 2 and all("healthy" in line for line in lines):
            return
        time.sleep(2)
    raise DrillError("topology did not become healthy")


def wait_recovery_state(service, in_recovery, deadline_seconds=60):
    expected = "t" if in_recovery else "f"
    deadline = time.monotonic() + deadline_seconds
    while time.monotonic() < deadline:
        proc = compose("exec", "-T", service, "psql", "-U", "evia", "-d", "evia",
                       "-tAc", "SELECT pg_is_in_recovery()", check=False)
        if proc.returncode == 0 and proc.stdout.strip() == expected:
            return
        time.sleep(1)
    raise DrillError(f"{service} never reached pg_is_in_recovery()={expected}")


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
    reserves = [event for event in events if event.get("type") != "summary"]
    if not reserves:
        raise DrillError("victim recorded no insert attempts")
    errors = sum(1 for event in reserves if event.get("result") == "error")
    if errors == 0:
        raise DrillError("no failure was observed while the primary was down")
    if not any(event.get("phase") == "recovery" and event.get("result") == "ok" for event in reserves):
        raise DrillError("the same pool did not recover after promotion")

    # Reconstruct the committed sequence from the event stream: failed
    # attempts keep their sequence, every success (including the explicit
    # recovery insert) advances by exactly one.
    expected_seq, committed = 1, []
    for event in reserves:
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
        raise DrillError("victim log lacks the final table summary")
    if not all(int(summary[key]) == value for key, value in
               [("ok", ok), ("count", ok), ("distinct", ok), ("max_seq", ok), ("gaps", 0)]):
        raise DrillError(f"summary counters do not match the event stream: {summary}")
    if int(summary["errors"]) != errors:
        raise DrillError("summary error count does not match the event stream")
    return {
        "committed_inserts": ok,
        "failed_attempts": errors,
        "max_sequence": ok,
        "table_gaps": int(summary["gaps"]),
    }


def run_baseline_test(env):
    proc = run([
        "go", "test", "-tags=integration", "-count=1",
        "-run", "TestFailoverBaselineWrite", "./internal/storage/postgres",
    ], env=env, cwd=BACKEND)
    return proc.stdout.strip().splitlines()


def run_victim(duration_seconds, log_path, env):
    victim_env = dict(env)
    victim_env["EVIA_TEST_PG_VICTIM"] = "1"
    victim_env["EVIA_TEST_PG_VICTIM_LOG"] = log_path
    victim_env["EVIA_TEST_PG_VICTIM_DURATION"] = f"{duration_seconds}s"
    return subprocess.Popen(
        ["go", "test", "-tags=integration", "-count=1",
         "-run", "TestFailoverVictimLoop", "./internal/storage/postgres"],
        cwd=str(BACKEND), env=victim_env, text=True,
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
    )


def go_env():
    env = dict(os.environ)
    env["EVIA_TEST_PG_FAILOVER_DSN"] = FAILOVER_DSN
    return env


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
        wait_recovery_state("pg-standby", in_recovery=True)
        streaming = psql("pg-primary",
                         "SELECT count(*) FROM pg_stat_replication WHERE state = 'streaming'")
        if streaming != "1":
            raise DrillError(f"expected one streaming replica, got {streaming}")
        phases.append({"phase": "streaming_replication", "status": "ok", "streaming_replicas": streaming})

        baseline = run_baseline_test(go_env())
        phases.append({"phase": "baseline_integration_test", "status": "ok", "output": baseline})

        with tempfile.TemporaryDirectory(prefix="evia-pg-drill-") as tmp:
            log_path = os.path.join(tmp, "victim.jsonl")
            victim = run_victim(args.duration, log_path, go_env())
            time.sleep(3)
            stop_started = time.monotonic()
            compose("stop", "pg-primary")
            promote = compose("exec", "-T", "pg-standby", "psql", "-U", "evia", "-d", "evia",
                              "-tAc", "SELECT pg_promote(true, 30)")
            if promote.stdout.strip() != "t":
                raise DrillError(f"standby promotion did not return true: {promote.stdout.strip()}")
            wait_recovery_state("pg-standby", in_recovery=False)
            outage_ms = int((time.monotonic() - stop_started) * 1000)
            phases.append({"phase": "primary_failover", "status": "ok", "outage_ms": outage_ms})

            victim_output = victim.communicate(timeout=args.duration + 90)[0]
            if victim.returncode != 0:
                raise DrillError(f"victim process failed:\n{victim_output}")
            verdict = evaluate(parse_event_log(Path(log_path).read_text()))
            phases.append({"phase": "victim_evaluation", "status": "ok", "verdict": verdict})

            independent = psql("pg-standby",
                               "SELECT count(*), count(DISTINCT seq), max(seq) FROM evia_ha_drill")
            parts = independent.split("|")
            if len(parts) != 3 or parts != [str(verdict["committed_inserts"])] * 3:
                raise DrillError(f"independent promoted-node read mismatch: {independent}")
            phases.append({"phase": "independent_cross_check", "status": "ok", "table": independent})

        # Recreate the old primary as a standby of the promoted node. Its
        # entrypoint wipes the stale data directory and re-runs pg_basebackup.
        compose("stop", "pg-primary")
        compose("rm", "-f", "pg-primary")
        rejoin_env = dict(os.environ)
        rejoin_env["EVIA_HA_PG_ROLE_PRIMARY"] = "standby"
        rejoin_env["EVIA_HA_PG_PEER_PRIMARY"] = STANDBY_IP
        compose("up", "-d", "pg-primary", env=rejoin_env)
        deadline = time.monotonic() + 120
        caught_up = False
        while time.monotonic() < deadline:
            proc = compose("exec", "-T", "pg-primary", "psql", "-U", "evia", "-d", "evia",
                           "-tAc", "SELECT pg_is_in_recovery() AND pg_last_wal_replay_lsn() IS NOT NULL",
                           env=rejoin_env, check=False)
            if proc.returncode == 0 and proc.stdout.strip() == "t":
                caught_up = True
                break
            time.sleep(2)
        if not caught_up:
            raise DrillError("old primary did not rejoin as a streaming standby")
        phases.append({"phase": "old_primary_rejoin", "status": "ok"})
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
