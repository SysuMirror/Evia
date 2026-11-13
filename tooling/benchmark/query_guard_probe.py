#!/usr/bin/env python3
"""Query guard-rail completeness and cancellation isolation probe.

The tenant isolation probe covered the memory (Code 241) and read-row
(Code 158) guards. This probe closes the rest of the closed analysis-query
settings map enforced by backend/internal/analysis/clickhouse.go:

* readonly=2 write protection;
* max_execution_time timeout;
* max_result_rows / max_result_bytes result-set overflow;
* the KILL QUERY ASYNC path the Go engine fires when a caller context is
  cancelled, proving a cancelled query actually stops consuming the server
  and cannot affect a sibling query.

Empirical ClickHouse 26.8 codes established up front and intentionally NOT
guessed: READONLY=164, TIMEOUT_EXCEEDED=159, TOO_MANY_ROWS_OR_BYTES=396
(result rows and bytes share this code), TOO_MANY_BYTES=307 (read bytes),
TOO_MANY_ROWS=158 (read rows), MEMORY_LIMIT_EXCEEDED=241,
QUERY_WAS_CANCELLED=394. Because codes are reused across distinct guards
(396 covers both result limits, 159 is timeout here), classification is driven
by the parenthesised exception TYPE NAME with the code as corroboration.

Every query carries the identical production settings map (imported from the
tenant isolation probe so there is one source of truth). All classification
and verdict math are pure functions unit-tested without ClickHouse; only the
harness talks to the database, which needs no persistent table because every
guard fires on the fly.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from pathlib import Path

from property_layout_probe import ClickHouseProbe, ProbeError
from tenant_query_isolation_probe import PRODUCTION_QUERY_SETTINGS, settings_clause

ROOT = Path(__file__).resolve().parents[2]
DEFAULT_CLIENT = "docker exec evia-benchmark-clickhouse-1 clickhouse-client"
QUERY_ID_PREFIX = "evia-guard"
PROCESS_POLL_TIMEOUT_S = 30
PROCESS_POLL_INTERVAL_S = 0.1
SCRATCH_DATABASE = "evia_guard_scratch"


# Empirically verified ClickHouse 26.8 exception codes.
CODE_READONLY = 164
CODE_TIMEOUT = 159
CODE_RESULT_LIMIT = 396
CODE_READ_BYTES = 307
CODE_READ_ROWS = 158
CODE_MEMORY = 241
CODE_CANCELLED = 394

# Exception TYPE NAME tokens (authoritative because codes are reused).
TYPE_READONLY = "READONLY"
TYPE_TIMEOUT = "TIMEOUT_EXCEEDED"
TYPE_RESULT = "TOO_MANY_ROWS_OR_BYTES"
TYPE_READ_BYTES = "TOO_MANY_BYTES"
TYPE_READ_ROWS = "TOO_MANY_ROWS"
TYPE_MEMORY = "MEMORY_LIMIT_EXCEEDED"
TYPE_CANCELLED = "QUERY_WAS_CANCELLED"

LABEL_READONLY = "readonly_write_rejected"
LABEL_TIMEOUT = "execution_timeout"
LABEL_RESULT_ROWS = "result_rows_limit_exceeded"
LABEL_RESULT_BYTES = "result_bytes_limit_exceeded"
LABEL_CANCELLED = "query_cancelled"
LABEL_READ_ROWS = "read_rows_limit_exceeded"
LABEL_READ_BYTES = "read_bytes_limit_exceeded"
LABEL_MEMORY = "memory_limit_exceeded"
LABEL_OTHER = "other_error"
LABEL_OK = "ok"

_TYPE_TO_LABEL = {
    TYPE_READONLY: LABEL_READONLY,
    TYPE_TIMEOUT: LABEL_TIMEOUT,
    TYPE_RESULT: LABEL_RESULT_ROWS,  # rows/bytes disambiguated separately
    TYPE_CANCELLED: LABEL_CANCELLED,
}
_CODE_RE = re.compile(r"Code:\s*(\d+)")
_TYPE_RE = re.compile(r"\(([A-Z_]+)\)")


# ---------------------------------------------------------------------------
# Pure exception classification
# ---------------------------------------------------------------------------

def extract_exception_code(message: str) -> int | None:
    match = _CODE_RE.search(message or "")
    return int(match.group(1)) if match else None


def extract_exception_type(message: str) -> str | None:
    match = _TYPE_RE.search(message or "")
    return match.group(1) if match else None


def classify_guard(message: str) -> str:
    """Classify by exception type name first (codes are reused), then code."""
    type_name = extract_exception_type(message)
    code = extract_exception_code(message)

    if type_name == TYPE_READONLY or code == CODE_READONLY:
        return LABEL_READONLY
    if type_name == TYPE_TIMEOUT or code == CODE_TIMEOUT:
        return LABEL_TIMEOUT
    if type_name == TYPE_CANCELLED or code == CODE_CANCELLED:
        return LABEL_CANCELLED
    if type_name == TYPE_RESULT or code == CODE_RESULT_LIMIT:
        # The message distinguishes result rows vs bytes.
        if "max bytes" in message:
            return LABEL_RESULT_BYTES
        return LABEL_RESULT_ROWS
    if type_name == TYPE_READ_BYTES or code == CODE_READ_BYTES:
        return LABEL_READ_BYTES
    if type_name == TYPE_READ_ROWS or code == CODE_READ_ROWS:
        return LABEL_READ_ROWS
    if type_name == TYPE_MEMORY or code == CODE_MEMORY:
        return LABEL_MEMORY
    return LABEL_OTHER


# ---------------------------------------------------------------------------
# Pure SQL builders (each carries the production settings map)
# ---------------------------------------------------------------------------

def with_production_settings(sql: str, overrides: dict[str, str] | None = None) -> str:
    settings = dict(PRODUCTION_QUERY_SETTINGS)
    if overrides:
        settings.update(overrides)
    return f"{sql} SETTINGS " + ", ".join(
        f"{key}={value}" for key, value in settings.items())


def readonly_write_sql() -> str:
    """A DDL that readonly=2 must reject before any object is created. The
    scratch database exists, so the readonly guard (not a missing-database
    error) is what fires."""
    return with_production_settings(
        f"CREATE TABLE {SCRATCH_DATABASE}.forbidden (a UInt8) ENGINE=Memory")


def timeout_sql() -> str:
    """A two-second sleep bounded to one second, overriding only the time
    budget while keeping every other production guard. A row-counting query
    would hit the 2M-row read cap before the 30s production timeout, so sleep
    is used to isolate the execution-time guard deterministically."""
    return with_production_settings(
        "SELECT sleep(2)", overrides={"max_execution_time": "1"})


def result_rows_overflow_sql() -> str:
    """Emit more rows than max_result_rows=100001."""
    return with_production_settings("SELECT number FROM numbers(200000)")


def result_bytes_overflow_sql() -> str:
    """Exceed max_result_bytes=4MiB while staying under the row cap. The
    strings are materialized into one groupArray column; a streaming SELECT
    does not accrue against max_result_bytes the same way."""
    return with_production_settings(
        "SELECT groupArray(repeat('x', 100)) FROM numbers(100000)")


def long_running_sql(query_id: str) -> str:
    """Long enough to be observed in system.processes and then cancelled.
    max_execution_time is raised locally so the KILL, not the timeout, fires."""
    if not re.fullmatch(r"[a-z0-9\-]+", query_id):
        raise ProbeError(f"unsafe query id {query_id!r}")
    return ("SELECT count() FROM numbers(100000000000) "
            "SETTINGS max_execution_time=120, max_threads=2")


def kill_query_sql(target_query_id: str) -> str:
    if not re.fullmatch(r"[a-z0-9\-]+", target_query_id):
        raise ProbeError(f"unsafe kill target {target_query_id!r}")
    return f"KILL QUERY WHERE query_id='{target_query_id}' ASYNC"


def sibling_sql() -> str:
    """A cheap bounded query that must survive the neighbour's cancellation."""
    return with_production_settings("SELECT count() FROM numbers(1000)")


# ---------------------------------------------------------------------------
# Pure verdicts
# ---------------------------------------------------------------------------

@dataclass
class GuardOutcome:
    name: str
    succeeded: bool
    label: str
    code: int | None
    duration_ms: float


@dataclass
class CancellationResult:
    long_query_seen_before_kill: bool
    process_gone_after_kill: bool
    long_query_label: str
    long_query_code: int | None
    sibling_succeeded: bool
    sibling_label: str


def evaluate_guard_outcome(outcome: GuardOutcome, expected_label: str) -> list[str]:
    failures = []
    if outcome.succeeded:
        failures.append(f"{outcome.name} guard did not reject the query")
    if outcome.label != expected_label:
        failures.append(
            f"{outcome.name} classified as {outcome.label!r}, want {expected_label!r}")
    return failures


def evaluate_cancellation(result: CancellationResult) -> list[str]:
    failures = []
    if not result.long_query_seen_before_kill:
        failures.append("long query never appeared in system.processes before KILL")
    if not result.process_gone_after_kill:
        failures.append("long query remained in system.processes after KILL")
    if result.long_query_label != LABEL_CANCELLED:
        failures.append(
            f"long query ended as {result.long_query_label!r}, want {LABEL_CANCELLED!r}")
    if not result.sibling_succeeded:
        failures.append(f"sibling query did not survive: {result.sibling_label}")
    return failures


# ---------------------------------------------------------------------------
# ClickHouse harness
# ---------------------------------------------------------------------------

class GuardHarness:
    def __init__(self, client: ClickHouseProbe):
        self.client = client

    def provision_scratch(self) -> None:
        # No readonly setting here: the scratch db must exist so the readonly
        # CREATE TABLE attempt fails on the guard rather than a missing object.
        self.client.run(f"DROP DATABASE IF EXISTS {SCRATCH_DATABASE} SYNC")
        self.client.run(f"CREATE DATABASE {SCRATCH_DATABASE}")

    def drop_scratch(self) -> None:
        self.client.run(f"DROP DATABASE IF EXISTS {SCRATCH_DATABASE} SYNC")

    def run_guarded(self, name: str, sql: str, query_id: str) -> GuardOutcome:
        started = time.monotonic()
        try:
            self.client.run(sql, query_id=query_id)
            return GuardOutcome(name, True, LABEL_OK, None,
                                (time.monotonic() - started) * 1000)
        except ProbeError as exc:
            message = str(exc)
            return GuardOutcome(name, False, classify_guard(message),
                                extract_exception_code(message),
                                (time.monotonic() - started) * 1000)

    def process_present(self, query_id: str) -> bool:
        value = self.client.scalar(
            "SELECT count() FROM system.processes WHERE query_id='"
            + query_id + "' FORMAT TSV")
        return int(value) > 0

    def wait_for_process(self, query_id: str) -> bool:
        deadline = time.monotonic() + PROCESS_POLL_TIMEOUT_S
        while time.monotonic() < deadline:
            if self.process_present(query_id):
                return True
            time.sleep(PROCESS_POLL_INTERVAL_S)
        return False

    def wait_for_process_gone(self, query_id: str) -> bool:
        deadline = time.monotonic() + PROCESS_POLL_TIMEOUT_S
        while time.monotonic() < deadline:
            if not self.process_present(query_id):
                return True
            time.sleep(PROCESS_POLL_INTERVAL_S)
        return False

    def cancellation_scenario(self) -> CancellationResult:
        long_id = f"{QUERY_ID_PREFIX}-long"
        long_outcome: dict = {}

        def run_long() -> None:
            try:
                self.client.run(long_running_sql(long_id), query_id=long_id)
                long_outcome["label"] = LABEL_OK
                long_outcome["code"] = None
            except ProbeError as exc:
                long_outcome["label"] = classify_guard(str(exc))
                long_outcome["code"] = extract_exception_code(str(exc))

        worker = threading.Thread(target=run_long, daemon=True)
        worker.start()
        seen = self.wait_for_process(long_id)
        # Issue the production-shaped async kill from the control path.
        self.client.run(kill_query_sql(long_id))
        gone = self.wait_for_process_gone(long_id)
        worker.join(timeout=PROCESS_POLL_TIMEOUT_S)

        # Sibling query runs after the kill to prove cancellation is scoped.
        sibling = self.run_guarded(
            "cancellation-sibling", sibling_sql(), f"{QUERY_ID_PREFIX}-sib")
        return CancellationResult(
            long_query_seen_before_kill=seen,
            process_gone_after_kill=gone,
            long_query_label=long_outcome.get("label", LABEL_OTHER),
            long_query_code=long_outcome.get("code"),
            sibling_succeeded=sibling.succeeded,
            sibling_label=sibling.label)


def run_probe(client: ClickHouseProbe, keep: bool) -> dict:
    harness = GuardHarness(client)
    harness.provision_scratch()
    scenarios = []
    verdicts = []
    try:
        return _run_scenarios(harness)
    finally:
        if not keep:
            harness.drop_scratch()


def _run_scenarios(harness: GuardHarness) -> dict:
    scenarios = []
    verdicts = []

    guard_cases = [
        ("readonly-write", readonly_write_sql(), LABEL_READONLY),
        ("execution-timeout", timeout_sql(), LABEL_TIMEOUT),
        ("result-rows-overflow", result_rows_overflow_sql(), LABEL_RESULT_ROWS),
        ("result-bytes-overflow", result_bytes_overflow_sql(), LABEL_RESULT_BYTES),
    ]
    for name, sql, expected in guard_cases:
        outcome = harness.run_guarded(name, sql, f"{QUERY_ID_PREFIX}-{name}")
        failures = evaluate_guard_outcome(outcome, expected)
        scenarios.append({
            "name": name, "expected": expected, "observed": outcome.label,
            "code": outcome.code, "duration_ms": round(outcome.duration_ms, 2),
            "rejected": not outcome.succeeded})
        verdicts.append({"name": name, "passed": not failures, "failures": failures})

    cancellation = harness.cancellation_scenario()
    cancel_failures = evaluate_cancellation(cancellation)
    scenarios.append({
        "name": "kill-query-cancellation",
        "seen_before_kill": cancellation.long_query_seen_before_kill,
        "gone_after_kill": cancellation.process_gone_after_kill,
        "long_query_label": cancellation.long_query_label,
        "long_query_code": cancellation.long_query_code,
        "sibling_succeeded": cancellation.sibling_succeeded,
        "sibling_label": cancellation.sibling_label})
    verdicts.append({"name": "kill-query-cancellation",
                     "passed": not cancel_failures, "failures": cancel_failures})

    return {
        "status": "passed" if all(item["passed"] for item in verdicts) else "failed",
        "probe": "query-guard-rails",
        "settings": PRODUCTION_QUERY_SETTINGS,
        "scenarios": scenarios,
        "verdicts": verdicts,
    }


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--client", default=os.environ.get("EVIA_CH_CLIENT", DEFAULT_CLIENT))
    parser.add_argument("--username", default=os.environ.get("EVIA_CH_USERNAME", "evia"))
    parser.add_argument("--password", default=os.environ.get("EVIA_CH_PASSWORD", ""))
    parser.add_argument("--keep", action="store_true")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    client = ClickHouseProbe(args.client, args.username, args.password)
    report = run_probe(client, args.keep)
    print(json.dumps(report, indent=2, sort_keys=True))
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
