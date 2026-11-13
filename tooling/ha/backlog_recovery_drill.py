#!/usr/bin/env python3
"""Kafka backlog recovery drill for the Evia ingest pipeline.

The drill drives the production Kafka -> ingest-worker -> ClickHouse path
through a bounded sink outage and proves the backlog/drain contract:

  1. While the sink is unavailable the worker retries the held batch without
     committing offsets, so the consumer group lag grows to the full backlog
     and stays frozen (no silent offset advance, no data landing).
  2. After the sink recovers the same worker drains the backlog, lag returns
     to zero, and every produced event lands exactly once: the dedup view
     count equals the produced count and the raw table never has fewer rows.

The fault is injected in-process by a gate sink that returns
ErrSinkUnavailable, the same error the production sink surfaces when
ClickHouse is down. Real Kafka supplies the offset/lag semantics and real
ClickHouse the final exact-once landing. Only the Python standard library is
used for the orchestration and the gate-tested math; container lifecycle is
owned by the shared test stack, not this script.
"""

import argparse
import json
import os
import subprocess
import sys
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
BACKEND = ROOT / "backend"
DEFAULT_BROKERS = "127.0.0.1:19092"
DEFAULT_CLICKHOUSE_URL = "http://127.0.0.1:18123"


class DrillError(RuntimeError):
    """A backlog recovery invariant could not be proven."""


# ---------------------------------------------------------------------------
# Pure logic (unit-tested without docker)
# ---------------------------------------------------------------------------

def drain_rate(backlog_events, drain_ms):
    """Events per second drained after the sink recovered."""
    if not isinstance(backlog_events, int) or backlog_events <= 0:
        raise DrillError("backlog event count must be a positive integer")
    if not isinstance(drain_ms, int) or drain_ms <= 0:
        raise DrillError("drain duration must be a positive integer of milliseconds")
    return backlog_events / (drain_ms / 1000.0)


def summarize_lag_samples(samples):
    """Reduce an outage lag series to its peak and freeze behavior."""
    if not samples:
        raise DrillError("at least one lag sample is required")
    if any(not isinstance(value, int) or value < 0 for value in samples):
        raise DrillError(f"lag samples must be non-negative integers: {samples!r}")
    return {
        "samples": len(samples),
        "peak": max(samples),
        "frozen": len(set(samples)) == 1,
        "minimum": min(samples),
    }


def parse_backlog_summary(output):
    """Extract the Go backlog summary regardless of surrounding test logs."""
    for line in output.splitlines():
        compact = line.replace(" ", "")
        if '"type":"backlog_recovery_summary"' in compact:
            return json.loads(line)
    return None


def evaluate_backlog(summary):
    """Apply the exact-once, freeze and bounded-drain invariants."""
    if not isinstance(summary, dict):
        raise DrillError("missing backlog summary")
    required = ("produced", "baseline", "backlog", "peak_lag", "final_lag",
                "lag_samples", "drain_ms", "view_count", "raw_count", "sink_attempts")
    missing = [key for key in required if key not in summary]
    if missing:
        raise DrillError(f"summary missing fields: {missing}")

    produced = summary["produced"]
    backlog = summary["backlog"]
    if produced != summary["baseline"] + backlog:
        raise DrillError("produced count does not equal baseline plus backlog")
    if summary["final_lag"] != 0:
        raise DrillError(f"consumer lag did not return to zero: {summary['final_lag']}")
    lag = summarize_lag_samples(summary["lag_samples"])
    if not lag["frozen"]:
        raise DrillError(f"lag was not frozen during the outage: {summary['lag_samples']}")
    if lag["peak"] < backlog // 2:
        raise DrillError(
            f"peak lag {lag['peak']} did not reflect the backlog of {backlog}")
    if summary["view_count"] != produced:
        raise DrillError(
            f"dedup view landed {summary['view_count']} rows, produced {produced}")
    if summary["raw_count"] < produced:
        raise DrillError(
            f"raw table has {summary['raw_count']} rows, fewer than {produced} produced")
    if summary["sink_attempts"] <= 0:
        raise DrillError("the gate sink recorded no write attempts")
    rate = drain_rate(backlog, summary["drain_ms"])
    expected_rate = summary.get("drain_rate_per_s")
    if expected_rate is not None and abs(rate - float(expected_rate)) > 1.0:
        raise DrillError(
            f"reported drain rate {expected_rate} disagrees with computed {rate:.3f}")
    return {
        "produced": produced,
        "peak_lag": lag["peak"],
        "drain_ms": summary["drain_ms"],
        "drain_rate_per_s": round(rate, 3),
        "exact_once": summary["view_count"] == produced and summary["raw_count"] >= produced,
        "frozen_during_outage": lag["frozen"],
    }


# ---------------------------------------------------------------------------
# Orchestration
# ---------------------------------------------------------------------------

def require_env():
    brokers = os.environ.get("EVIA_TEST_KAFKA_BROKERS", DEFAULT_BROKERS)
    ch_url = os.environ.get("EVIA_TEST_CLICKHOUSE_URL", DEFAULT_CLICKHOUSE_URL)
    user = os.environ.get("EVIA_TEST_CLICKHOUSE_USERNAME")
    password = os.environ.get("EVIA_TEST_CLICKHOUSE_PASSWORD")
    if not user or not password:
        raise DrillError(
            "EVIA_TEST_CLICKHOUSE_USERNAME and EVIA_TEST_CLICKHOUSE_PASSWORD are required")
    return brokers, ch_url, user, password


def check_tcp(host_port, timeout_seconds=5):
    import socket
    host, port = host_port.rsplit(":", 1)
    with socket.create_connection((host, int(port)), timeout=timeout_seconds):
        return True


def check_clickhouse(url, user, password, timeout_seconds=10):
    import urllib.request
    import urllib.error
    from urllib.parse import urlencode
    request = urllib.request.Request(url + "?" + urlencode({"query": "SELECT 1"}))
    request.add_header("Authorization", "Basic " + _basic_auth(user, password))
    with urllib.request.urlopen(request, timeout=timeout_seconds) as response:
        return response.status == 200


def _basic_auth(user, password):
    import base64
    return base64.b64encode(f"{user}:{password}".encode()).decode()


def run_go_drill(env):
    proc = subprocess.run(
        ["go", "test", "-tags", "integration", "-count=1", "-v",
         "-run", "TestBacklogRecoveryUnderSinkOutage", "./internal/pipeline/"],
        cwd=str(BACKEND), env=env, text=True,
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
    )
    return proc


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--keep-output", action="store_true",
                        help="print the full Go test output on failure")
    args = parser.parse_args(argv)

    brokers, ch_url, user, password = require_env()
    phases = []
    try:
        check_tcp(brokers.split(",")[0])
        phases.append({"phase": "kafka_reachable", "status": "ok", "brokers": brokers})
        check_clickhouse(ch_url, user, password)
        phases.append({"phase": "clickhouse_reachable", "status": "ok", "url": ch_url})

        env = dict(os.environ)
        env["EVIA_TEST_BACKLOG_DRILL"] = "1"
        env.setdefault("EVIA_TEST_KAFKA_BROKERS", brokers)
        env.setdefault("EVIA_TEST_CLICKHOUSE_URL", ch_url)
        started = time.monotonic()
        proc = run_go_drill(env)
        elapsed_ms = int((time.monotonic() - started) * 1000)
        if proc.returncode != 0:
            tail = proc.stdout if args.keep_output else "\n".join(proc.stdout.splitlines()[-20:])
            raise DrillError(f"Go backlog drill failed (rc={proc.returncode}):\n{tail}")

        summary = parse_backlog_summary(proc.stdout)
        verdict = evaluate_backlog(summary)
        phases.append({"phase": "backlog_recovery", "status": "ok",
                       "elapsed_ms": elapsed_ms, "verdict": verdict,
                       "sink_attempts": summary["sink_attempts"],
                       "outage_ms": summary["outage_ms"]})
    finally:
        pass

    report = {
        "status": "passed",
        "project": "evia-dev-pipeline",
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
