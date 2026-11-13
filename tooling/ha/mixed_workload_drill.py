#!/usr/bin/env python3
"""Steady mixed read/write workload drill for the Evia storage stack.

The drill drives a Go integration test that runs a sustained Kafka producer
and ingest worker while four reader goroutines issue bounded analytics
against the same ClickHouse tables. It proves the steady-state invariants that
the read-only query matrix cannot observe on a frozen dataset:

  1. Every produced event lands exactly once while reads are in flight.
  2. No read fails under concurrent write pressure.
  3. Count observations per reader are monotonically non-decreasing for the
     insert-only workload.
  4. Read latency stays inside the per-query execution budget and the
     ClickHouse-side peak memory is reported from system.query_log.

Only the Python standard library is used for orchestration and the gate-tested
math; the storage workload itself runs in Go against the shared test stack.
"""

import argparse
import json
import os
import socket
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
BACKEND = ROOT / "backend"
DEFAULT_BROKERS = "127.0.0.1:19092"
DEFAULT_CLICKHOUSE_URL = "http://127.0.0.1:18123"


class DrillError(RuntimeError):
    """A mixed-workload invariant could not be proven."""


# ---------------------------------------------------------------------------
# Pure logic (unit-tested without docker)
# ---------------------------------------------------------------------------

def percentile(values, pct):
    """Nearest-rank percentile matching the Go drill's index convention."""
    if not isinstance(pct, int) or not 0 <= pct <= 100:
        raise DrillError("percentile must be an integer between 0 and 100")
    if not values:
        raise DrillError("at least one latency sample is required")
    if any(not isinstance(value, int) or value < 0 for value in values):
        raise DrillError(f"latency samples must be non-negative integers: {values!r}")
    ordered = sorted(values)
    index = (len(ordered) * pct) // 100
    if index >= len(ordered):
        index = len(ordered) - 1
    return ordered[index]


def is_monotonic_non_decreasing(series):
    if not series:
        raise DrillError("a count trail must not be empty")
    if any(not isinstance(value, int) for value in series):
        raise DrillError(f"count trail must contain integers: {series!r}")
    return all(later >= earlier for earlier, later in zip(series, series[1:]))


def success_rate(read_samples, read_errors):
    if not isinstance(read_samples, int) or read_samples <= 0:
        raise DrillError("read sample count must be a positive integer")
    if not isinstance(read_errors, int) or read_errors < 0:
        raise DrillError("read error count must be a non-negative integer")
    total = read_samples + read_errors
    return read_samples / total


def parse_mixed_summary(output):
    """Extract the Go mixed-workload summary regardless of surrounding logs."""
    for line in output.splitlines():
        compact = line.replace(" ", "")
        if '"type":"mixed_workload_summary"' in compact:
            return json.loads(line)
    return None


def evaluate_mixed(summary, latency_bound_ms):
    """Apply the exact-once, zero-failure, monotonic and latency invariants."""
    if not isinstance(summary, dict):
        raise DrillError("missing mixed-workload summary")
    required = ("produced", "view_count", "raw_count", "read_samples", "read_errors",
                "p50_read_ms", "p95_read_ms", "peak_query_memory", "monotonic_readers",
                "reader_goroutines", "drain_ms")
    missing = [key for key in required if key not in summary]
    if missing:
        raise DrillError(f"summary missing fields: {missing}")

    if summary["view_count"] != summary["produced"]:
        raise DrillError(
            f"dedup view {summary['view_count']} != produced {summary['produced']}")
    if summary["raw_count"] < summary["produced"]:
        raise DrillError("raw table has fewer rows than produced events")
    if summary["read_errors"] != 0:
        raise DrillError(f"read errors under mixed load: {summary['read_errors']}")
    if summary["read_samples"] <= 0:
        raise DrillError("no read samples were collected")
    rate = success_rate(summary["read_samples"], summary["read_errors"])
    if rate != 1.0:
        raise DrillError(f"read success rate {rate} is not 1.0")
    if summary["monotonic_readers"] != summary["reader_goroutines"]:
        raise DrillError(
            f"only {summary['monotonic_readers']} of {summary['reader_goroutines']} "
            "readers observed monotonic counts")
    if summary["p95_read_ms"] > latency_bound_ms:
        raise DrillError(
            f"p95 read latency {summary['p95_read_ms']}ms exceeds bound {latency_bound_ms}ms")
    if summary["peak_query_memory"] <= 0:
        raise DrillError("query_log reported no peak memory")
    return {
        "produced": summary["produced"],
        "read_samples": summary["read_samples"],
        "read_success_rate": rate,
        "p50_read_ms": summary["p50_read_ms"],
        "p95_read_ms": summary["p95_read_ms"],
        "peak_query_memory": summary["peak_query_memory"],
        "exact_once": summary["view_count"] == summary["produced"]
        and summary["raw_count"] >= summary["produced"],
        "write_rate_per_s": round(summary["produced"] / (summary["drain_ms"] / 1000.0), 3),
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
    host, port = host_port.rsplit(":", 1)
    with socket.create_connection((host, int(port)), timeout=timeout_seconds):
        return True


def check_clickhouse(url, user, password, timeout_seconds=10):
    import base64
    from urllib.parse import urlencode
    request = urllib.request.Request(url + "?" + urlencode({"query": "SELECT 1"}))
    token = base64.b64encode(f"{user}:{password}".encode()).decode()
    request.add_header("Authorization", "Basic " + token)
    with urllib.request.urlopen(request, timeout=timeout_seconds) as response:
        return response.status == 200


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--latency-bound-ms", type=int, default=10000,
                        help="maximum acceptable p95 read latency")
    parser.add_argument("--keep-output", action="store_true")
    args = parser.parse_args(argv)

    brokers, ch_url, user, password = require_env()
    phases = []
    check_tcp(brokers.split(",")[0])
    phases.append({"phase": "kafka_reachable", "status": "ok", "brokers": brokers})
    check_clickhouse(ch_url, user, password)
    phases.append({"phase": "clickhouse_reachable", "status": "ok", "url": ch_url})

    env = dict(os.environ)
    env["EVIA_TEST_MIXED_DRILL"] = "1"
    env.setdefault("EVIA_TEST_KAFKA_BROKERS", brokers)
    env.setdefault("EVIA_TEST_CLICKHOUSE_URL", ch_url)
    started = time.monotonic()
    proc = subprocess.run(
        ["go", "test", "-tags", "integration", "-count=1", "-v",
         "-run", "TestSteadyMixedReadWrite", "./internal/pipeline/"],
        cwd=str(BACKEND), env=env, text=True,
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
    )
    elapsed_ms = int((time.monotonic() - started) * 1000)
    if proc.returncode != 0:
        tail = proc.stdout if args.keep_output else "\n".join(proc.stdout.splitlines()[-20:])
        raise DrillError(f"Go mixed-workload drill failed (rc={proc.returncode}):\n{tail}")

    summary = parse_mixed_summary(proc.stdout)
    verdict = evaluate_mixed(summary, args.latency_bound_ms)
    phases.append({"phase": "steady_mixed_read_write", "status": "ok",
                   "elapsed_ms": elapsed_ms, "verdict": verdict,
                   "per_shape_p95_ms": summary.get("per_shape_p95_ms", {})})

    print(json.dumps({"status": "passed", "project": "evia-dev-pipeline",
                      "phases": phases}, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except DrillError as exc:
        print(json.dumps({"status": "failed", "error": str(exc)}, indent=2), file=sys.stderr)
        sys.exit(1)
