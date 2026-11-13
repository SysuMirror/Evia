#!/usr/bin/env python3
"""High-cardinality long-tail distribution drill for the Evia storage stack.

The read-only 514/728 matrix and the mixed-workload drill both use shallow,
near-uniform identity distributions. This drill models a realistic long tail:
a small set of hot identities absorbs most events while a large sparse set
contributes the rest, then proves the storage layer answers both hot-point and
long-tail queries correctly and within bounds.

The distribution rule is deterministic and dependency-free so the Go loader
and this Python evaluator derive identical expectations without sharing a
fixture file:

* the first ``hot_events = round(N * hot_share)`` events rotate evenly across
  ``hot_count`` hot identities;
* every remaining event rotates evenly across the
  ``cardinality - hot_count`` sparse identities.

Only the Python standard library is used for the gate-tested math; the actual
load and measurements run in Go against the shared Kafka/ClickHouse stack.
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
    """A long-tail distribution invariant could not be proven."""


# ---------------------------------------------------------------------------
# Pure distribution logic (unit-tested, mirrored by the Go loader)
# ---------------------------------------------------------------------------

def hot_event_count(total, hot_share):
    if not isinstance(total, int) or total <= 0:
        raise DrillError("total events must be a positive integer")
    if not (0.0 < hot_share < 1.0):
        raise DrillError("hot_share must be strictly between 0 and 1")
    return round(total * hot_share)


def assign_uid_index(event_ordinal, total, cardinality, hot_count, hot_share):
    """Return the deterministic identity index for one zero-based event."""
    if not isinstance(cardinality, int) or not isinstance(hot_count, int):
        raise DrillError("cardinality and hot_count must be integers")
    if not (1 <= hot_count < cardinality):
        raise DrillError("require 1 <= hot_count < cardinality")
    if not (0 <= event_ordinal < total):
        raise DrillError("event ordinal out of range")
    hot_events = hot_event_count(total, hot_share)
    if event_ordinal < hot_events:
        return event_ordinal % hot_count
    sparse = cardinality - hot_count
    return hot_count + ((event_ordinal - hot_events) % sparse)


def expected_counts(total, cardinality, hot_count, hot_share):
    """Exact per-identity event counts implied by the deterministic rule."""
    counts = [0] * cardinality
    for ordinal in range(total):
        counts[assign_uid_index(ordinal, total, cardinality, hot_count, hot_share)] += 1
    return counts


def concentration_metrics(counts, hot_count):
    """Summarize head concentration and sparse-tail coverage."""
    if not counts or any(not isinstance(value, int) or value < 0 for value in counts):
        raise DrillError("counts must be non-negative integers")
    total = sum(counts)
    if total == 0:
        raise DrillError("cannot measure concentration over zero events")
    if not (1 <= hot_count <= len(counts)):
        raise DrillError("hot_count outside the identity range")
    hot = sorted(counts, reverse=True)[:hot_count]
    observed = sorted(counts, reverse=True)
    covered = sum(1 for value in counts if value > 0)
    return {
        "total": total,
        "cardinality": len(counts),
        "covered_identities": covered,
        "hot_share_observed": round(sum(hot) / total, 6),
        "hottest_count": observed[0],
        "sparsest_nonzero_count": min(value for value in counts if value > 0),
        "head_to_tail_ratio": round(observed[0] / min(value for value in counts if value > 0), 6),
    }


def evaluate_long_tail(summary, latency_bound_ms):
    """Validate the Go report against distribution exactness and read bounds."""
    if not isinstance(summary, dict):
        raise DrillError("missing long-tail summary")
    required = ("type", "total_events", "cardinality", "hot_count", "hot_share",
                "covered_identities", "hot_share_observed", "hot_query",
                "tail_query", "cardinality_query", "exact_counts_match",
                "peak_query_memory")
    missing = [key for key in required if key not in summary]
    if missing:
        raise DrillError(f"summary missing fields: {missing}")
    if summary["type"] != "long_tail_summary":
        raise DrillError("unexpected summary type")
    if summary["covered_identities"] != summary["cardinality"]:
        raise DrillError(
            f"only {summary['covered_identities']} of {summary['cardinality']} identities landed")
    if not summary["exact_counts_match"]:
        raise DrillError("per-identity ClickHouse counts diverged from the distribution")
    expected_share = round(summary["hot_share"], 6)
    if abs(summary["hot_share_observed"] - expected_share) > 0.01:
        raise DrillError(
            f"observed hot share {summary['hot_share_observed']} != expected {expected_share}")
    for query_name in ("hot_query", "tail_query", "cardinality_query"):
        query = summary[query_name]
        if query.get("errors", 1) != 0:
            raise DrillError(f"{query_name} reported errors: {query}")
        if query["p95_ms"] > latency_bound_ms:
            raise DrillError(
                f"{query_name} p95 {query['p95_ms']}ms exceeds bound {latency_bound_ms}ms")
        if query["samples"] <= 0:
            raise DrillError(f"{query_name} collected no samples")
    if summary["peak_query_memory"] <= 0:
        raise DrillError("query_log reported no peak memory")
    return {
        "total_events": summary["total_events"],
        "cardinality": summary["cardinality"],
        "covered_identities": summary["covered_identities"],
        "hot_share_observed": summary["hot_share_observed"],
        "hot_p95_ms": summary["hot_query"]["p95_ms"],
        "tail_p95_ms": summary["tail_query"]["p95_ms"],
        "cardinality_p95_ms": summary["cardinality_query"]["p95_ms"],
        "exact_counts_match": summary["exact_counts_match"],
    }


def parse_long_tail_summary(output):
    for line in output.splitlines():
        compact = line.replace(" ", "")
        if '"type":"long_tail_summary"' in compact:
            return json.loads(line)
    return None


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
    parser.add_argument("--latency-bound-ms", type=int, default=10000)
    parser.add_argument("--keep-output", action="store_true")
    args = parser.parse_args(argv)

    brokers, ch_url, user, password = require_env()
    phases = []
    check_tcp(brokers.split(",")[0])
    phases.append({"phase": "kafka_reachable", "status": "ok", "brokers": brokers})
    check_clickhouse(ch_url, user, password)
    phases.append({"phase": "clickhouse_reachable", "status": "ok", "url": ch_url})

    env = dict(os.environ)
    env["EVIA_TEST_LONGTAIL_DRILL"] = "1"
    env.setdefault("EVIA_TEST_KAFKA_BROKERS", brokers)
    env.setdefault("EVIA_TEST_CLICKHOUSE_URL", ch_url)
    started = time.monotonic()
    proc = subprocess.run(
        ["go", "test", "-tags", "integration", "-count=1", "-v",
         "-run", "TestHighCardinalityLongTail", "./internal/pipeline/"],
        cwd=str(BACKEND), env=env, text=True,
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
    )
    elapsed_ms = int((time.monotonic() - started) * 1000)
    if proc.returncode != 0:
        tail = proc.stdout if args.keep_output else "\n".join(proc.stdout.splitlines()[-20:])
        raise DrillError(f"Go long-tail drill failed (rc={proc.returncode}):\n{tail}")

    summary = parse_long_tail_summary(proc.stdout)
    verdict = evaluate_long_tail(summary, args.latency_bound_ms)
    phases.append({"phase": "high_cardinality_long_tail", "status": "ok",
                   "elapsed_ms": elapsed_ms, "verdict": verdict})

    print(json.dumps({"status": "passed", "project": "evia-dev-pipeline",
                      "phases": phases}, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except DrillError as exc:
        print(json.dumps({"status": "failed", "error": str(exc)}, indent=2), file=sys.stderr)
        sys.exit(1)
