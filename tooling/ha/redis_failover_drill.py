#!/usr/bin/env python3
"""Redis Sentinel failover drill for the Evia admission path.

The script brings up the standalone 1-master / 1-replica / 3-sentinel
topology from deploy/compose.ha-redis.yaml, runs the Go integration clients
through Sentinel discovery, stops the master to force a real promotion, and
proves three invariants on the live system:

  1. The long-lived budget client fails closed while no master is available;
     no ambiguous debit is replayed (MaxRetries=-1 survives Sentinel mode).
  2. The same process resumes successful reserves after Sentinel promotes the
     replica, without restart or reconfiguration.
  3. Redis-side token accounting matches every accepted reserve exactly,
     within the negligible floor-rate refill band, so debits never double.

Only the Python standard library is used. Container control goes through the
docker and redis-cli CLIs; all state is torn down with ``compose down -v``.
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
COMPOSE_FILE = ROOT / "deploy" / "compose.ha-redis.yaml"
PROJECT = "evia-ha-redis"
MASTER_NAME = "evia-redis"
SENTINEL_PORTS = (26379, 26380, 26381)
SENTINEL_SEED_ADDRS = ",".join(f"127.0.0.1:{port}" for port in SENTINEL_PORTS)
EVENT_COST = 1_000_000


class DrillError(RuntimeError):
    """A failover invariant could not be proven."""


def run(command, *, cwd=None, env=None, check=True, capture=True):
    """Run one command and return its decoded stdout, failing closed."""
    proc = subprocess.run(
        command,
        cwd=str(cwd) if cwd else None,
        env=env,
        text=True,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.PIPE if capture else None,
    )
    if check and proc.returncode != 0:
        raise DrillError(
            f"command failed ({proc.returncode}): {' '.join(command)}\n{proc.stderr.strip()}"
        )
    return proc


def compose(*args):
    return run(["docker", "compose", "-f", str(COMPOSE_FILE), "-p", PROJECT, *args])


def redis_cli(port, *args):
    return run(["redis-cli", "-p", str(port), *args]).stdout.split()


def master_addr():
    """Return the (ip, port) Sentinel currently advertises for the master."""
    values = redis_cli(SENTINEL_PORTS[0], "sentinel", "get-master-addr-by-name", MASTER_NAME)
    if len(values) != 2:
        return None, None
    return values[0], values[1]


def wait_master_agreement(deadline_seconds=90):
    deadline = time.monotonic() + deadline_seconds
    while time.monotonic() < deadline:
        addresses = set()
        for port in SENTINEL_PORTS:
            values = redis_cli(port, "sentinel", "get-master-addr-by-name", MASTER_NAME)
            if len(values) == 2:
                addresses.add((values[0], values[1]))
        replicas = redis_cli(SENTINEL_PORTS[0], "sentinel", "replicas", MASTER_NAME)
        if len(addresses) == 1 and replicas:
            return next(iter(addresses))
        time.sleep(1)
    raise DrillError("sentinel quorum never agreed on one master with a known replica")


def wait_master_change(previous, deadline_seconds=60):
    deadline = time.monotonic() + deadline_seconds
    while time.monotonic() < deadline:
        current = master_addr()
        if current != previous and current[0] is not None:
            return current
        time.sleep(0.5)
    raise DrillError("sentinel did not promote the replica within the deadline")


def go_env():
    env = dict(os.environ)
    env["EVIA_TEST_REDIS_SENTINEL_ADDRS"] = SENTINEL_SEED_ADDRS
    env["EVIA_TEST_REDIS_SENTINEL_MASTER"] = MASTER_NAME
    return env


def run_baseline_tests():
    proc = run(
        [
            "go",
            "test",
            "-tags=integration",
            "-count=1",
            "-run",
            "TestSentinelBaseline",
            "./internal/platform/budget",
            "./internal/platform/admissionstats",
        ],
        cwd=BACKEND,
        env=go_env(),
    )
    return proc.stdout.strip().splitlines()


def run_victim(duration_seconds, burst, log_path):
    env = go_env()
    env["EVIA_TEST_SENTINEL_VICTIM"] = "1"
    env["EVIA_TEST_SENTINEL_VICTIM_LOG"] = log_path
    env["EVIA_TEST_SENTINEL_VICTIM_DURATION"] = f"{duration_seconds}s"
    env["EVIA_TEST_SENTINEL_VICTIM_BURST"] = str(burst)
    return subprocess.Popen(
        [
            "go",
            "test",
            "-tags=integration",
            "-count=1",
            "-run",
            "TestSentinelVictimLoop",
            "./internal/platform/budget",
        ],
        cwd=str(BACKEND),
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
    )


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


def evaluate(events, burst, duration_ms):
    """Apply the failover invariants to one victim JSONL event stream."""
    summary = next((event for event in events if event.get("type") == "summary"), None)
    reserves = [event for event in events if event.get("type") != "summary"]
    if not reserves:
        raise DrillError("victim recorded no reserve attempts")
    ok = sum(1 for event in reserves if event.get("result") == "ok")
    errors = sum(1 for event in reserves if event.get("result") == "error")
    if errors == 0:
        raise DrillError("no failure was observed while the master was down")
    if not any(event.get("code") == "COLLECTION_BUDGET_UNAVAILABLE" for event in reserves):
        raise DrillError("observed errors never reached the fail-closed budget-unavailable path")
    if not any(event.get("phase") == "recovery" and event.get("result") == "ok" for event in reserves):
        raise DrillError("the same process did not recover after failover")
    if ok > burst:
        raise DrillError(f"accepted {ok} reserves beyond the burst of {burst}")
    if summary is None:
        raise DrillError("victim log lacks the final bucket summary")
    if int(summary["ok"]) != ok or int(summary["errors"]) != errors:
        raise DrillError("summary counters do not match the recorded event stream")
    consumed = int(summary["capacity"]) - int(summary["tokens"])
    refill_per_ms = int(summary["refill_per_ms"])
    lower = ok * EVENT_COST - duration_ms * refill_per_ms
    upper = ok * EVENT_COST
    if not lower <= consumed <= upper:
        raise DrillError(f"consumed units {consumed} outside the exact debit band [{lower}, {upper}]")
    return {
        "accepted_reserves": ok,
        "failed_reserves": errors,
        "consumed_token_units": consumed,
        "exact_debit_band": [lower, upper],
        "bucket_key": summary["key"],
    }


def cross_check_bucket(bucket_key, expected_tokens):
    ip, port = master_addr()
    values = run(["redis-cli", "-h", ip, "-p", port, "HGETALL", bucket_key]).stdout.split()
    mapping = {values[i]: values[i + 1] for i in range(0, len(values), 2)}
    if mapping.get("tokens") != str(expected_tokens):
        raise DrillError(
            f"independent bucket read {mapping.get('tokens')} != victim summary {expected_tokens}"
        )
    return mapping


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--duration", type=int, default=45, help="victim loop duration in seconds")
    parser.add_argument("--burst", type=int, default=1000, help="fixed token burst for the victim")
    parser.add_argument("--keep", action="store_true", help="leave the topology running for inspection")
    args = parser.parse_args(argv)

    phases = []
    started = False
    topology_removed = False
    try:
        compose("up", "-d")
        started = True
        phases.append({"phase": "compose_up", "status": "ok"})

        original = wait_master_agreement()
        phases.append({"phase": "sentinel_agreement", "status": "ok", "master": original[0]})

        baseline = run_baseline_tests()
        phases.append({"phase": "baseline_integration_tests", "status": "ok", "output": baseline})

        with tempfile.TemporaryDirectory(prefix="evia-ha-drill-") as tmp:
            log_path = os.path.join(tmp, "victim.jsonl")
            victim = run_victim(args.duration, args.burst, log_path)
            time.sleep(3)
            stop_started = time.monotonic()
            compose("stop", "redis-master")
            promoted = wait_master_change(original)
            outage_ms = int((time.monotonic() - stop_started) * 1000)
            phases.append({"phase": "master_failover", "status": "ok", "promoted": promoted[0], "outage_ms": outage_ms})

            victim_output = victim.communicate(timeout=args.duration + 60)[0]
            if victim.returncode != 0:
                raise DrillError(f"victim process failed:\n{victim_output}")
            events = parse_event_log(Path(log_path).read_text())
            verdict = evaluate(events, args.burst, args.duration * 1000)
            phases.append({"phase": "victim_evaluation", "status": "ok", "verdict": verdict})

            compose("start", "redis-master")
            deadline = time.monotonic() + 60
            healthy = False
            while time.monotonic() < deadline:
                proc = run(["redis-cli", "-p", "16390", "info", "replication"], check=False)
                if proc.returncode == 0 and "role:slave" in proc.stdout and "master_link_status:up" in proc.stdout:
                    healthy = True
                    break
                time.sleep(1)
            if not healthy:
                raise DrillError("restored master did not rejoin as a healthy replica")
            phases.append({"phase": "old_master_rejoin", "status": "ok"})

            summary = next(event for event in events if event.get("type") == "summary")
            bucket = cross_check_bucket(verdict["bucket_key"], int(summary["tokens"]))
            phases.append({"phase": "independent_bucket_cross_check", "status": "ok", "bucket": bucket})
    finally:
        if started and not args.keep:
            compose("down", "-v")
            topology_removed = True

    report = {
        "status": "passed",
        "project": PROJECT,
        "duration_seconds": args.duration,
        "burst": args.burst,
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
