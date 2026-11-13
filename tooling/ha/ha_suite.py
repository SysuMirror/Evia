#!/usr/bin/env python3
"""High-availability and capacity readiness suite for Evia.

The HA and capacity work produced ten standalone drills, each with its own
compose topology or shared test stack and its own JSON report. Operators
preparing a release previously had to invoke them one by one and reconcile the
results manually. This suite turns them into one repeatable readiness gate:

* a declarative registry records every drill, its tier, compose topology,
  required environment variables and a bounded timeout;
* preconditions are evaluated before execution, so a missing credential is a
  deliberate skip with a reason, not a crash;
* each drill runs in an isolated subprocess with its own timeout, and one
  failure never blocks the remaining drills;
* the run ends with one machine-readable report and a deterministic exit code
  (non-zero only when at least one selected drill failed; skips do not fail).

Only the Python standard library is used. The selection, precondition and
aggregation math is pure and unit-tested; subprocess orchestration is the only
side-effecting layer.
"""

import argparse
import json
import os
import shutil
import subprocess
import sys
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
HA_DIR = ROOT / "tooling" / "ha"

# Tier identifiers. Topology drills own a dedicated compose project and need
# the Docker daemon; dev-stack drills target the already running test
# containers and need ClickHouse credentials.
TIER_TOPOLOGY = "topology"
TIER_DEV_STACK = "dev-stack"

# Credentials without an in-script default: the suite must fail closed when
# these are absent. URL and broker variables have sensible loopback defaults
# inside each drill, so they are recorded for documentation but not required.
DEV_CLICKHOUSE_ENV = (
    "EVIA_TEST_CLICKHOUSE_USERNAME",
    "EVIA_TEST_CLICKHOUSE_PASSWORD",
)
DEV_OPTIONAL_ENV = (
    "EVIA_TEST_CLICKHOUSE_URL",
    "EVIA_TEST_KAFKA_BROKERS",
)


class SuiteError(RuntimeError):
    """The suite configuration or registry itself is invalid."""


# Each entry is deliberately data-only so selection and precondition logic can
# be unit-tested without touching the filesystem or the Docker daemon.
DRILLS = (
    {
        "name": "redis-sentinel",
        "script": "redis_failover_drill.py",
        "tier": TIER_TOPOLOGY,
        "compose": "compose.ha-redis.yaml",
        "required_env": (),
        "timeout_seconds": 420,
    },
    {
        "name": "redis-cluster",
        "script": "redis_cluster_failover_drill.py",
        "tier": TIER_TOPOLOGY,
        "compose": "compose.ha-redis-cluster.yaml",
        "required_env": (),
        "timeout_seconds": 420,
    },
    {
        "name": "postgres-failover",
        "script": "postgres_failover_drill.py",
        "tier": TIER_TOPOLOGY,
        "compose": "compose.ha-postgres.yaml",
        "required_env": (),
        "timeout_seconds": 420,
    },
    {
        "name": "postgres-quorum",
        "script": "postgres_quorum_drill.py",
        "tier": TIER_TOPOLOGY,
        "compose": "compose.ha-postgres-quorum.yaml",
        "required_env": (),
        "timeout_seconds": 480,
    },
    {
        "name": "clickhouse-failover",
        "script": "clickhouse_failover_drill.py",
        "tier": TIER_TOPOLOGY,
        "compose": "compose.ha-clickhouse.yaml",
        "required_env": (),
        "timeout_seconds": 480,
    },
    {
        "name": "clickhouse-keeper-quorum",
        "script": "clickhouse_keeper_quorum_drill.py",
        "tier": TIER_TOPOLOGY,
        "compose": "compose.ha-clickhouse.yaml",
        "required_env": (),
        "timeout_seconds": 480,
    },
    {
        "name": "backlog-recovery",
        "script": "backlog_recovery_drill.py",
        "tier": TIER_DEV_STACK,
        "compose": None,
        "required_env": DEV_CLICKHOUSE_ENV,
        "timeout_seconds": 240,
    },
    {
        "name": "mixed-read-write",
        "script": "mixed_workload_drill.py",
        "tier": TIER_DEV_STACK,
        "compose": None,
        "required_env": DEV_CLICKHOUSE_ENV,
        "timeout_seconds": 240,
    },
    {
        "name": "long-tail",
        "script": "long_tail_drill.py",
        "tier": TIER_DEV_STACK,
        "compose": None,
        "required_env": DEV_CLICKHOUSE_ENV,
        "timeout_seconds": 240,
    },    {
        "name": "zipf-distribution",
        "script": "zipf_distribution_drill.py",
        "tier": TIER_DEV_STACK,
        "compose": None,
        "required_env": DEV_CLICKHOUSE_ENV,
        "timeout_seconds": 240,
    },
)


# ---------------------------------------------------------------------------
# Pure selection and precondition logic
# ---------------------------------------------------------------------------

def validate_registry(drills):
    """Fail closed on duplicate names, missing scripts or bad tier values."""
    names = set()
    for drill in drills:
        for key in ("name", "script", "tier", "timeout_seconds"):
            if key not in drill:
                raise SuiteError(f"drill entry missing {key}: {drill!r}")
        if drill["name"] in names:
            raise SuiteError(f"duplicate drill name: {drill['name']}")
        names.add(drill["name"])
        if drill["tier"] not in (TIER_TOPOLOGY, TIER_DEV_STACK):
            raise SuiteError(f"unknown tier for {drill['name']}: {drill['tier']}")
        if not isinstance(drill["timeout_seconds"], int) or drill["timeout_seconds"] <= 0:
            raise SuiteError(f"invalid timeout for {drill['name']}")
    return True


def select_drills(drills, only=None, exclude=None, tier=None):
    """Apply name include/exclude and tier filters in a deterministic order."""
    only = set(only or ())
    exclude = set(exclude or ())
    unknown = (only | exclude) - {drill["name"] for drill in drills}
    if unknown:
        raise SuiteError(f"unknown drill names: {sorted(unknown)}")
    selected = []
    for drill in drills:
        name = drill["name"]
        if only and name not in only:
            continue
        if name in exclude:
            continue
        if tier and drill["tier"] != tier:
            continue
        selected.append(drill)
    return selected


def missing_env(required, env):
    """Return the sorted required environment variables that are unset/blank."""
    return sorted(name for name in required if not env.get(name))


def evaluate_preconditions(drill, env, docker_available):
    """Decide run vs skip without side effects; returns (runnable, reason)."""
    missing = missing_env(drill.get("required_env", ()), env)
    if missing:
        return False, "missing environment variables: " + ", ".join(missing)
    if drill["tier"] == TIER_TOPOLOGY and not docker_available:
        return False, "Docker daemon is not available"
    return True, ""


def summarize(results):
    """Reduce per-drill outcomes to stable counters and an overall verdict."""
    counts = {"passed": 0, "failed": 0, "skipped": 0}
    for result in results:
        status = result["status"]
        if status not in counts:
            raise SuiteError(f"unknown result status: {status}")
        counts[status] += 1
    counts["total"] = len(results)
    counts["verdict"] = "passed" if counts["failed"] == 0 else "failed"
    return counts


# ---------------------------------------------------------------------------
# Side-effecting orchestration
# ---------------------------------------------------------------------------

def docker_is_available():
    return shutil.which("docker") is not None


def run_drill(drill, env):
    """Execute one drill as an isolated, timeout-bounded subprocess."""
    script = HA_DIR / drill["script"]
    started = time.monotonic()
    try:
        proc = subprocess.run(
            [sys.executable, str(script)],
            cwd=str(ROOT), env=env, text=True,
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            timeout=drill["timeout_seconds"],
        )
        elapsed_ms = int((time.monotonic() - started) * 1000)
        return {
            "name": drill["name"], "tier": drill["tier"],
            "status": "passed" if proc.returncode == 0 else "failed",
            "elapsed_ms": elapsed_ms, "returncode": proc.returncode,
            "output_tail": "\n".join(proc.stdout.splitlines()[-15:]),
        }
    except subprocess.TimeoutExpired:
        return {
            "name": drill["name"], "tier": drill["tier"], "status": "failed",
            "elapsed_ms": drill["timeout_seconds"] * 1000, "returncode": None,
            "output_tail": f"timed out after {drill['timeout_seconds']}s",
        }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--only", help="comma-separated drill names to run")
    parser.add_argument("--exclude", help="comma-separated drill names to skip")
    parser.add_argument("--tier", choices=(TIER_TOPOLOGY, TIER_DEV_STACK),
                        help="limit the suite to one tier")
    parser.add_argument("--report", help="write the JSON report to this path")
    parser.add_argument("--dry-run", action="store_true",
                        help="resolve selection and preconditions without executing drills")
    args = parser.parse_args(argv)

    validate_registry(DRILLS)
    only = [value.strip() for value in args.only.split(",")] if args.only else None
    exclude = [value.strip() for value in args.exclude.split(",")] if args.exclude else None
    selected = select_drills(DRILLS, only=only, exclude=exclude, tier=args.tier)
    if not selected:
        raise SuiteError("no drills selected")

    docker_available = docker_is_available()
    env = dict(os.environ)
    results = []
    for drill in selected:
        runnable, reason = evaluate_preconditions(drill, env, docker_available)
        if not runnable:
            results.append({"name": drill["name"], "tier": drill["tier"],
                            "status": "skipped", "reason": reason})
            continue
        if args.dry_run:
            results.append({"name": drill["name"], "tier": drill["tier"],
                            "status": "skipped", "reason": "dry-run"})
            continue
        results.append(run_drill(drill, env))

    summary = summarize(results)
    report = {"status": summary["verdict"], "dry_run": bool(args.dry_run),
              "summary": summary, "results": results}
    rendered = json.dumps(report, indent=2, sort_keys=True)
    if args.report:
        Path(args.report).write_text(rendered + "\n", encoding="utf-8")
    print(rendered)
    return 0 if summary["failed"] == 0 else 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except SuiteError as exc:
        print(json.dumps({"status": "failed", "error": str(exc)}, indent=2), file=sys.stderr)
        sys.exit(2)
