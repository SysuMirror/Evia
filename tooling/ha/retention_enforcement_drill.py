#!/usr/bin/env python3
"""Long-retention enforcement drill for the Evia storage stack.

The retention domain (policy config, sweep worker, archive-before-delete) and
the ClickHouse RetentionExecutor already have unit and scoped integration
tests. This drill closes the remaining capacity question: when a real
SweepWorker drives the real executor across multiple tenants, does the
synchronous ALTER DELETE mutation actually reclaim physical disk, how much
transient headroom does the old/new part coexistence consume, and can a
policy-less or in-window scope be damaged by an enforced pass?

The Go test (TestRetentionEnforcementCapacity) owns all interactions with
ClickHouse and emits one retention_enforcement_summary JSON line. This
evaluator only validates that summary with dependency-free logic so the gate
can run inside ``make ha-tools-test`` without a database:

* dry-run counts exactly the aged rows and never mutates facts;
* enforcement deletes exactly those rows, one mutation per configured scope,
  all finished synchronously;
* re-sweeping creates zero new mutations (idempotent);
* table bytes shrink while the measured coexistence peak never falls below
  the baseline (headroom evidence);
* recent rows and the policy-less aged scope are byte-exact preserved and the
  audit ledger records one dry-run plus two enforced evaluations per scope.

Disk-wide free space is reported but never asserted: concurrent system-table
writes make it too noisy for a closed gate. Table-level bytes are the
authoritative reclamation signal.
"""

import argparse
import base64
import json
import os
import socket
import subprocess
import sys
import time
import urllib.request
from pathlib import Path
from urllib.parse import urlencode

ROOT = Path(__file__).resolve().parents[2]
BACKEND = ROOT / "backend"
DEFAULT_BROKERS = "127.0.0.1:19092"
DEFAULT_CLICKHOUSE_URL = "http://127.0.0.1:18123"

REQUIRED_FIELDS = (
    "type", "configured_scopes", "policy_less_scopes", "aged_rows_per_scope",
    "recent_rows_per_scope", "policy_less_rows", "total_inserted",
    "dry_run_candidates", "enforced_deleted", "rows_after_sweep",
    "retained_recent_rows", "mutations_total", "mutations_done",
    "resweep_mutations", "table_bytes_before", "table_bytes_peak",
    "table_bytes_after", "table_bytes_resweep", "reclaimed_bytes",
    "peak_amplification", "disk_free_before", "disk_free_after",
    "part_log", "audit_records", "isolation_intact", "policy_less_intact",
)


class DrillError(RuntimeError):
    """A retention enforcement invariant could not be proven."""


def parse_retention_summary(output):
    for line in output.splitlines():
        compact = line.replace(" ", "")
        if '"type":"retention_enforcement_summary"' in compact:
            return json.loads(line)
    return None


def evaluate_enforcement(summary):
    """Validate the enforced sweep capacity and isolation summary."""
    if not isinstance(summary, dict):
        raise DrillError("missing retention enforcement summary")
    missing = [key for key in REQUIRED_FIELDS if key not in summary]
    if missing:
        raise DrillError(f"summary missing fields: {missing}")
    if summary["type"] != "retention_enforcement_summary":
        raise DrillError("unexpected summary type")

    scopes = summary["configured_scopes"]
    if not isinstance(scopes, int) or scopes <= 0:
        raise DrillError("configured_scopes must be a positive integer")
    expected_expired = scopes * summary["aged_rows_per_scope"]
    expected_retained = scopes * summary["recent_rows_per_scope"] + summary["policy_less_rows"]

    if summary["dry_run_candidates"] != expected_expired:
        raise DrillError(
            f"dry-run candidates {summary['dry_run_candidates']} != expected {expected_expired}")
    if summary["enforced_deleted"] != expected_expired:
        raise DrillError(
            f"enforced deleted {summary['enforced_deleted']} != expected {expected_expired}")
    if summary["rows_after_sweep"] != expected_retained:
        raise DrillError(
            f"rows after sweep {summary['rows_after_sweep']} != retained {expected_retained}")
    if summary["total_inserted"] != summary["enforced_deleted"] + summary["rows_after_sweep"]:
        raise DrillError("inserted rows do not reconcile with deleted plus retained rows")
    if summary["retained_recent_rows"] != scopes * summary["recent_rows_per_scope"]:
        raise DrillError("retained recent row count is inconsistent")

    if summary["mutations_total"] != scopes or summary["mutations_done"] != scopes:
        raise DrillError(
            f"mutations {summary['mutations_total']}/{summary['mutations_done']} "
            f"must be {scopes}/{scopes} (one synchronous mutation per scope)")
    if summary["resweep_mutations"] != 0:
        raise DrillError("idempotent re-sweep must not create mutations")

    before = summary["table_bytes_before"]
    peak = summary["table_bytes_peak"]
    after = summary["table_bytes_after"]
    if summary["reclaimed_bytes"] != before - after:
        raise DrillError("reclaimed_bytes must equal before minus after table bytes")
    if summary["reclaimed_bytes"] <= 0:
        raise DrillError("enforced sweep did not reclaim any table bytes")
    if peak < before:
        raise DrillError("measured part coexistence peak cannot be below the baseline")
    if summary["peak_amplification"] != peak - before:
        raise DrillError("peak_amplification must equal peak minus baseline")
    if summary["table_bytes_resweep"] != after:
        raise DrillError("table bytes must stay stable across the idempotent re-sweep")

    # part_log is buffer-flushed and therefore corroborating evidence only;
    # the durable mutation gate is system.mutations (checked above). One
    # mutation rewrites every matching part, so MutatePart counts have no
    # useful closed bound: validate the shape and report the number.
    if not isinstance(summary["part_log"], dict):
        raise DrillError("part_log must be an object")
    mutate_parts = summary["part_log"].get("MutatePart", {}).get("parts", 0)

    audit = summary["audit_records"]
    if audit.get("dry_run") != scopes or audit.get("enforced") != scopes * 2:
        raise DrillError(
            f"audit records {audit} must hold {scopes} dry-run and {scopes * 2} enforced rows")

    if not summary["isolation_intact"] or not summary["policy_less_intact"]:
        raise DrillError("tenant or policy-less scope isolation was violated")

    return {
        "configured_scopes": scopes,
        "deleted_rows": summary["enforced_deleted"],
        "retained_rows": summary["rows_after_sweep"],
        "reclaimed_bytes": summary["reclaimed_bytes"],
        "peak_amplification_bytes": summary["peak_amplification"],
        "peak_to_baseline_ratio": round(peak / before, 2),
        "mutations": f"{summary['mutations_done']}/{summary['mutations_total']}",
        "resweep_mutations": summary["resweep_mutations"],
        "part_log_mutate_parts": mutate_parts,
        "isolation_intact": True,
    }


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
    request = urllib.request.Request(url + "?" + urlencode({"query": "SELECT 1"}))
    token = base64.b64encode(f"{user}:{password}".encode()).decode()
    request.add_header("Authorization", "Basic " + token)
    with urllib.request.urlopen(request, timeout=timeout_seconds) as response:
        return response.status == 200


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--keep-output", action="store_true")
    args = parser.parse_args(argv)

    brokers, ch_url, user, password = require_env()
    phases = []
    check_tcp(brokers.split(",")[0])
    phases.append({"phase": "kafka_reachable", "status": "ok", "brokers": brokers})
    check_clickhouse(ch_url, user, password)
    phases.append({"phase": "clickhouse_reachable", "status": "ok", "url": ch_url})

    env = dict(os.environ)
    env["EVIA_TEST_RETENTION_DRILL"] = "1"
    env.setdefault("EVIA_TEST_KAFKA_BROKERS", brokers)
    env.setdefault("EVIA_TEST_CLICKHOUSE_URL", ch_url)
    started = time.monotonic()
    proc = subprocess.run(
        ["go", "test", "-tags", "integration", "-count=1", "-v",
         "-run", "TestRetentionEnforcementCapacity", "./internal/pipeline/"],
        cwd=str(BACKEND), env=env, text=True,
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
    )
    elapsed_ms = int((time.monotonic() - started) * 1000)
    if proc.returncode != 0:
        tail = proc.stdout if args.keep_output else "\n".join(proc.stdout.splitlines()[-20:])
        raise DrillError(f"Go retention drill failed (rc={proc.returncode}):\n{tail}")

    summary = parse_retention_summary(proc.stdout)
    verdict = evaluate_enforcement(summary)
    phases.append({"phase": "retention_enforcement", "status": "ok",
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
