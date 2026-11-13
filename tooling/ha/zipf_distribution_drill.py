#!/usr/bin/env python3
"""Zipf (inverse power-law) distribution drill for the Evia storage stack.

The earlier long-tail drill used a deterministic two-segment uniform split:
hot identities rotated evenly among themselves and sparse identities among
themselves, which reproduces a head/tail size gap but not a power law. This
drill loads a true Zipf distribution where the probability of identity rank r
is proportional to 1 / r**exponent, then proves the storage layer preserves
both exact per-identity counts and the rank-frequency power-law shape.

The apportionment is deterministic and dependency-free so the Go loader and
this Python evaluator derive identical integer expectations without sharing a
fixture file:

* weights w_i = 1 / i**exponent for one-based ranks i = 1..cardinality;
* quotas = total * w_i / sum(w);
* the Hamilton (largest-remainder) method rounds quotas to integers that sum
  exactly to total, breaking ties by lowest rank.

Only the Python standard library is used for the gate-tested math; the load
and measurements run in Go against the shared Kafka/ClickHouse stack.
"""

import argparse
import json
import math
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
    """A Zipf distribution invariant could not be proven."""


# ---------------------------------------------------------------------------
# Pure distribution logic (unit-tested, mirrored by the Go loader)
# ---------------------------------------------------------------------------

def zipf_weights(cardinality, exponent):
    """Unnormalized inverse power-law weights for one-based ranks."""
    if not isinstance(cardinality, int) or cardinality < 2:
        raise DrillError("cardinality must be an integer >= 2")
    if not (0.0 < exponent <= 3.0):
        raise DrillError("Zipf exponent must be within (0, 3]")
    return [1.0 / (rank ** exponent) for rank in range(1, cardinality + 1)]


def hamilton_apportion(weights, total):
    """Round fractional quotas to integers summing exactly to total."""
    if not isinstance(total, int) or total <= 0:
        raise DrillError("total must be a positive integer")
    if total < len(weights):
        raise DrillError("total events must cover every identity at least once")
    weight_sum = sum(weights)
    if weight_sum <= 0:
        raise DrillError("weights must sum to a positive value")
    quotas = [total * weight / weight_sum for weight in weights]
    seats = [int(quota) for quota in quotas]
    leftover = total - sum(seats)
    if leftover < 0 or leftover > len(weights):
        raise DrillError("Hamilton apportionment produced an impossible remainder")
    # Stable ordering: equal remainders resolve to the lowest rank, matching
    # the Go loader's sort.SliceStable tie-break.
    order = sorted(range(len(weights)),
                   key=lambda index: quotas[index] - seats[index], reverse=True)
    for index in order[:leftover]:
        seats[index] += 1
    if sum(seats) != total:
        raise DrillError("apportioned seats do not sum to the event total")
    return seats


def log_log_slope(counts):
    """Least-squares slope of ln(count) against ln(rank); Zipf predicts -s."""
    if len(counts) < 2 or any(not isinstance(value, int) or value <= 0 for value in counts):
        raise DrillError("slope fitting needs at least two positive integer counts")
    xs = [math.log(rank) for rank in range(1, len(counts) + 1)]
    ys = [math.log(value) for value in counts]
    mean_x = sum(xs) / len(xs)
    mean_y = sum(ys) / len(ys)
    numerator = sum((x - mean_x) * (y - mean_y) for x, y in zip(xs, ys))
    denominator = sum((x - mean_x) ** 2 for x in xs)
    if denominator == 0:
        raise DrillError("zero variance in log ranks")
    return numerator / denominator


def head_tail_ratio(counts):
    positive = [value for value in counts if value > 0]
    if not positive:
        raise DrillError("no populated identities")
    return max(positive) / min(positive)


def parse_zipf_summary(output):
    for line in output.splitlines():
        compact = line.replace(" ", "")
        if '"type":"zipf_distribution_summary"' in compact:
            return json.loads(line)
    return None


def evaluate_zipf(summary, slope_tolerance):
    """Validate exact apportionment, power-law shape, coverage and read bounds."""
    if not isinstance(summary, dict):
        raise DrillError("missing Zipf summary")
    required = ("type", "total_events", "cardinality", "exponent", "covered_identities",
                "observed_slope", "exact_counts_match", "hottest_count", "coldest_count",
                "hot_query", "tail_query", "cardinality_query", "peak_query_memory")
    missing = [key for key in required if key not in summary]
    if missing:
        raise DrillError(f"summary missing fields: {missing}")
    if summary["type"] != "zipf_distribution_summary":
        raise DrillError("unexpected summary type")
    if summary["covered_identities"] != summary["cardinality"]:
        raise DrillError(
            f"only {summary['covered_identities']} of {summary['cardinality']} identities landed")
    if not summary["exact_counts_match"]:
        raise DrillError("per-identity ClickHouse counts diverged from the Zipf apportionment")
    expected_slope = -float(summary["exponent"])
    if abs(summary["observed_slope"] - expected_slope) > slope_tolerance:
        raise DrillError(
            f"log-log slope {summary['observed_slope']:.4f} deviates from {expected_slope} "
            f"by more than {slope_tolerance}")
    for query_name in ("hot_query", "tail_query", "cardinality_query"):
        query = summary[query_name]
        if query.get("errors", 1) != 0 or query.get("samples", 0) <= 0:
            raise DrillError(f"{query_name} did not complete cleanly: {query}")
        if query["p95_ms"] > 10000:
            raise DrillError(f"{query_name} p95 {query['p95_ms']}ms exceeds the 10s bound")
    if summary["peak_query_memory"] <= 0:
        raise DrillError("query_log reported no peak memory")
    return {
        "total_events": summary["total_events"],
        "cardinality": summary["cardinality"],
        "observed_slope": round(summary["observed_slope"], 4),
        "expected_slope": expected_slope,
        "head_tail_ratio": summary["hottest_count"] / max(summary["coldest_count"], 1),
        "hot_p95_ms": summary["hot_query"]["p95_ms"],
        "tail_p95_ms": summary["tail_query"]["p95_ms"],
        "cardinality_p95_ms": summary["cardinality_query"]["p95_ms"],
        "exact_counts_match": summary["exact_counts_match"],
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
    parser.add_argument("--slope-tolerance", type=float, default=0.15,
                        help="maximum accepted deviation from the theoretical log-log slope")
    parser.add_argument("--keep-output", action="store_true")
    args = parser.parse_args(argv)

    brokers, ch_url, user, password = require_env()
    phases = []
    check_tcp(brokers.split(",")[0])
    phases.append({"phase": "kafka_reachable", "status": "ok", "brokers": brokers})
    check_clickhouse(ch_url, user, password)
    phases.append({"phase": "clickhouse_reachable", "status": "ok", "url": ch_url})

    env = dict(os.environ)
    env["EVIA_TEST_ZIPF_DRILL"] = "1"
    env.setdefault("EVIA_TEST_KAFKA_BROKERS", brokers)
    env.setdefault("EVIA_TEST_CLICKHOUSE_URL", ch_url)
    started = time.monotonic()
    proc = subprocess.run(
        ["go", "test", "-tags", "integration", "-count=1", "-v",
         "-run", "TestZipfDistribution", "./internal/pipeline/"],
        cwd=str(BACKEND), env=env, text=True,
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
    )
    elapsed_ms = int((time.monotonic() - started) * 1000)
    if proc.returncode != 0:
        tail = proc.stdout if args.keep_output else "\n".join(proc.stdout.splitlines()[-20:])
        raise DrillError(f"Go Zipf drill failed (rc={proc.returncode}):\n{tail}")

    summary = parse_zipf_summary(proc.stdout)
    verdict = evaluate_zipf(summary, args.slope_tolerance)
    phases.append({"phase": "zipf_distribution", "status": "ok",
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
