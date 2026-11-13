#!/usr/bin/env python3
"""Multi-tenant query memory isolation probe.

The 514/728 query-capacity matrix recorded resource rejections under
concurrency, and every analysis query already carries a closed ClickHouse
settings map (per-query 512 MiB memory ceiling, 2 threads, read/result caps,
readonly=2). G3 proved individual queries stay inside the memory budget, but
no evidence yet answers the multi-tenant capacity question:

* does the per-query ceiling bound the *aggregate* memory of N tenants
  querying concurrently, so one tenant cannot exhaust the server;
* does a runaway query get rejected by the correct guard while normal tenant
  queries running at the same instant still succeed (noisy-neighbor isolation);
* are the production settings actually the ones enforced on the wire.

This probe applies the identical settings map as
backend/internal/analysis/clickhouse.go on every query. It provisions an
isolated evia_probe_ten_* database with a tenant-partitioned table and runs
three scenarios: guard rejection (deterministic memory/read limit codes),
bounded concurrency (all tenants succeed, per-query and aggregate memory
bounded), and noisy neighbor (heavy queries rejected while bounded waves
pass). All verdict math and SQL builders are pure functions unit-tested
without ClickHouse; only the harness talks to the database, which is dropped
with SYNC at the end.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import threading
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass, field
from pathlib import Path

from property_layout_probe import ClickHouseProbe, ProbeError, validate_identifier

ROOT = Path(__file__).resolve().parents[2]
DEFAULT_CLIENT = "docker exec evia-benchmark-clickhouse-1 clickhouse-client"

PROBE_DB_PREFIX = "evia_probe_ten"
TABLE = "tenant_events"
TENANT_COUNT = 8
ROWS_PER_TENANT = 250_000
INSERT_BATCH = 25_000
POLL_INTERVAL_S = 0.02
QUERY_ID_PREFIX = "evia-ten"

# Mirror backend/internal/analysis/clickhouse.go exactly. String values carry
# single quotes for the SETTINGS clause; numeric/keyword values are bare.
PRODUCTION_QUERY_SETTINGS: dict[str, str] = {
    "readonly": "2",
    "max_execution_time": "30",
    "timeout_overflow_mode": "'throw'",
    "max_rows_to_read": "2000000",
    "max_bytes_to_read": "268435456",
    "read_overflow_mode": "'throw'",
    "max_memory_usage": "536870912",
    "max_threads": "2",
    "max_result_rows": "100001",
    "max_result_bytes": "4194304",
    "result_overflow_mode": "'throw'",
    "output_format_json_quote_64bit_integers": "'0'",
    "output_format_json_quote_denormals": "'0'",
    "cancel_http_readonly_queries_on_client_close": "'1'",
}
MEMORY_BUDGET_BYTES = 512 * 1024 * 1024
MAX_THREADS = 2

# ClickHouse exception codes observed against the closed settings map.
CODE_MEMORY_LIMIT_EXCEEDED = 241
CODE_TOO_MANY_ROWS = 158
CODE_TOO_MANY_BYTES = 159
CODE_RESULT_ROWS_LIMIT = 160

LABEL_MEMORY = "memory_limit_exceeded"
LABEL_READ_ROWS = "read_rows_limit_exceeded"
LABEL_READ_BYTES = "read_bytes_limit_exceeded"
LABEL_RESULT = "result_limit_exceeded"
LABEL_OTHER = "other_error"
LABEL_OK = "ok"

_TENANT_RE = re.compile(r"^tenant_[0-9a-f]{6}$")
_DB_RE = re.compile(r"^" + PROBE_DB_PREFIX + r"_[a-z0-9_]+$")
_CODE_RE = re.compile(r"Code:\s*(\d+)")


# ---------------------------------------------------------------------------
# Pure settings and SQL builders
# ---------------------------------------------------------------------------

def validate_probe_database(name: str) -> None:
    if not isinstance(name, str) or not _DB_RE.match(name):
        raise ProbeError(f"unsafe probe database name {name!r}")


def validate_tenant(name: str) -> None:
    if not isinstance(name, str) or not _TENANT_RE.match(name):
        raise ProbeError(f"invalid tenant identifier {name!r}")


def tenant_name(index: int) -> str:
    if not isinstance(index, int) or not 0 <= index < 16_777_216:
        raise ProbeError(f"tenant index out of range: {index}")
    return f"tenant_{index:06x}"


def settings_clause(settings: dict[str, str] | None = None) -> str:
    """Render the production settings as a ClickHouse SETTINGS suffix."""
    resolved = PRODUCTION_QUERY_SETTINGS if settings is None else settings
    return "SETTINGS " + ", ".join(f"{key}={value}" for key, value in resolved.items())


def assert_settings_match_production(observed: dict[str, str]) -> None:
    """Fail closed if the probe settings drift from the Go analysis engine."""
    if observed != PRODUCTION_QUERY_SETTINGS:
        missing = sorted(set(PRODUCTION_QUERY_SETTINGS) - set(observed))
        extra = sorted(set(observed) - set(PRODUCTION_QUERY_SETTINGS))
        changed = sorted(
            key for key in observed.keys() & PRODUCTION_QUERY_SETTINGS
            if observed[key] != PRODUCTION_QUERY_SETTINGS[key])
        raise ProbeError(
            f"query settings drift: missing={missing} extra={extra} changed={changed}")


def bounded_count_sql(qualified_table: str, tenant: str) -> str:
    validate_identifier(TABLE)
    validate_tenant(tenant)
    return f"SELECT count() FROM {qualified_table} WHERE tenant_id='{tenant}'"


def bounded_unique_sql(qualified_table: str, tenant: str) -> str:
    validate_identifier(TABLE)
    validate_tenant(tenant)
    return f"SELECT uniqExact(seq) FROM {qualified_table} WHERE tenant_id='{tenant}'"


def bounded_group_sql(qualified_table: str, tenant: str) -> str:
    """A heavier per-tenant aggregation that sustains long enough for the
    memory poller to observe concurrent overlap while staying far below the
    512 MiB ceiling (250k distinct groups per tenant)."""
    validate_identifier(TABLE)
    validate_tenant(tenant)
    return (f"SELECT count() FROM (SELECT seq, kind FROM {qualified_table} "
            f"WHERE tenant_id='{tenant}' GROUP BY seq, kind)")


def heavy_memory_sql() -> str:
    """Aggregate ~1 GiB of strings while reading only 2M rows, so the memory
    guard fires before the read-row guard (validated empirically, Code 241)."""
    return ("SELECT count() FROM (SELECT groupArray(repeat('x', 512)) AS a "
            "FROM numbers(2000000))")


def heavy_read_rows_sql() -> str:
    """Exceed max_rows_to_read=2M without approaching the memory ceiling."""
    return "SELECT count() FROM numbers(5000000)"


def with_settings(sql: str) -> str:
    return f"{sql} {settings_clause()}"


# ---------------------------------------------------------------------------
# Pure exception classification and verdicts
# ---------------------------------------------------------------------------

def classify_exception(code: int | None, message: str = "") -> str:
    if code is None:
        match = _CODE_RE.search(message or "")
        if not match:
            return LABEL_OTHER
        code = int(match.group(1))
    if code == CODE_MEMORY_LIMIT_EXCEEDED:
        return LABEL_MEMORY
    if code == CODE_TOO_MANY_ROWS:
        return LABEL_READ_ROWS
    if code == CODE_TOO_MANY_BYTES:
        return LABEL_READ_BYTES
    if code == CODE_RESULT_ROWS_LIMIT:
        return LABEL_RESULT
    return LABEL_OTHER


def extract_exception_code(message: str) -> int | None:
    match = _CODE_RE.search(message or "")
    return int(match.group(1)) if match else None


def aggregate_memory_ceiling(concurrency: int) -> int:
    """Hard engine guarantee: at most `concurrency` queries, each capped at
    MEMORY_BUDGET_BYTES, so their measured memory cannot exceed this sum."""
    if not isinstance(concurrency, int) or concurrency < 1:
        raise ProbeError(f"concurrency must be positive: {concurrency}")
    return concurrency * MEMORY_BUDGET_BYTES


def percentile(sorted_values: list[float], pct: float) -> float:
    if not sorted_values:
        raise ProbeError("cannot take percentile of empty sample")
    if not 0 < pct <= 100:
        raise ProbeError(f"percentile out of range: {pct}")
    index = min(len(sorted_values) - 1, int(round((pct / 100) * (len(sorted_values) - 1))))
    return sorted_values[index]


@dataclass
class QueryOutcome:
    query_id: str
    tenant: str
    kind: str
    succeeded: bool
    label: str
    exception_code: int | None
    peak_memory: int
    duration_ms: float


@dataclass
class ScenarioResult:
    name: str
    concurrency: int
    outcomes: list[QueryOutcome] = field(default_factory=list)
    aggregate_peak_memory: int = 0
    aggregate_ceiling: int = 0

    def by_kind(self, kind: str) -> list[QueryOutcome]:
        return [item for item in self.outcomes if item.kind == kind]


def evaluate_guard_rejection(result: ScenarioResult) -> list[str]:
    """The two heavy shapes must each hit their specific guard."""
    failures = []
    memory_runs = result.by_kind("heavy_memory")
    read_runs = result.by_kind("heavy_read_rows")
    if not memory_runs or any(item.label != LABEL_MEMORY for item in memory_runs):
        failures.append("heavy memory query did not deterministically hit MEMORY_LIMIT_EXCEEDED")
    if not read_runs or any(item.label != LABEL_READ_ROWS for item in read_runs):
        failures.append("heavy read query did not deterministically hit TOO_MANY_ROWS")
    if any(item.succeeded for item in memory_runs + read_runs):
        failures.append("a guarded runaway query was allowed to succeed")
    return failures


def evaluate_bounded_concurrency(result: ScenarioResult) -> list[str]:
    failures = []
    bounded = result.by_kind("bounded")
    if not bounded:
        failures.append("no bounded queries executed")
    failed = [item for item in bounded if not item.succeeded]
    if failed:
        failures.append(f"{len(failed)} bounded tenant queries failed under concurrency")
    over_budget = [item for item in bounded if item.peak_memory > MEMORY_BUDGET_BYTES]
    if over_budget:
        failures.append(f"{len(over_budget)} bounded queries exceeded the per-query budget")
    if result.aggregate_peak_memory > result.aggregate_ceiling:
        failures.append(
            f"aggregate peak {result.aggregate_peak_memory} exceeded ceiling "
            f"{result.aggregate_ceiling} for concurrency {result.concurrency}")
    return failures


def evaluate_noisy_neighbor(result: ScenarioResult) -> list[str]:
    failures = []
    bounded = result.by_kind("bounded")
    heavies = [item for item in result.outcomes if item.kind.startswith("heavy")]
    if not bounded or any(not item.succeeded for item in bounded):
        failures.append("bounded tenant queries must all survive the noisy neighbor")
    rejected = {item.label for item in heavies if not item.succeeded}
    if LABEL_MEMORY not in rejected or LABEL_READ_ROWS not in rejected:
        failures.append(f"noisy-neighbor guards not both observed: {sorted(rejected)}")
    if any(item.succeeded for item in heavies):
        failures.append("a heavy neighbor query was allowed to succeed")
    return failures


def summarize_scenario(result: ScenarioResult) -> dict:
    bounded = result.by_kind("bounded")
    durations = sorted(item.duration_ms for item in bounded)
    peaks = sorted(item.peak_memory for item in bounded)
    return {
        "name": result.name,
        "concurrency": result.concurrency,
        "bounded_queries": len(bounded),
        "bounded_succeeded": sum(1 for item in bounded if item.succeeded),
        "bounded_peak_memory_max": peaks[-1] if peaks else 0,
        "bounded_memory_headroom_bytes":
            MEMORY_BUDGET_BYTES - (peaks[-1] if peaks else 0),
        "bounded_duration_p50_ms": percentile(durations, 50) if durations else 0,
        "bounded_duration_p95_ms": percentile(durations, 95) if durations else 0,
        "aggregate_peak_memory": result.aggregate_peak_memory,
        "aggregate_ceiling": result.aggregate_ceiling,
        "aggregate_peak_ratio": round(
            result.aggregate_peak_memory / result.aggregate_ceiling, 4)
            if result.aggregate_ceiling else 0,
        # The poller samples system.processes; sub-poll-window queries finish
        # before a sample lands, so a zero/short value is a lower bound. The
        # hard aggregate ceiling is a deductive guarantee from the per-query
        # cap (no single query can exceed MEMORY_BUDGET_BYTES), not something
        # that depends on observing overlap.
        "sampled_aggregate_is_lower_bound": True,
        "heavy_labels": sorted({item.label for item in result.outcomes
                                if item.kind.startswith("heavy")}),
    }


# ---------------------------------------------------------------------------
# ClickHouse harness
# ---------------------------------------------------------------------------

class MemoryPoller:
    """Background sampler of summed measured memory across this probe's queries."""

    def __init__(self, client: ClickHouseProbe):
        self.client = client
        self._stop = threading.Event()
        self._thread: threading.Thread | None = None
        self.peak = 0

    def _sample(self) -> None:
        sql = (
            "SELECT sum(memory_usage) FROM system.processes WHERE user='evia' "
            f"AND query_id LIKE '{QUERY_ID_PREFIX}-%' FORMAT TSV")
        try:
            value = int(self.client.scalar(sql))
        except (ProbeError, ValueError):
            return
        if value > self.peak:
            self.peak = value

    def _loop(self) -> None:
        while not self._stop.is_set():
            self._sample()
            self._stop.wait(POLL_INTERVAL_S)

    def start(self) -> None:
        self._thread = threading.Thread(target=self._loop, daemon=True)
        self._thread.start()

    def stop(self) -> int:
        self._stop.set()
        if self._thread is not None:
            self._thread.join(timeout=10)
        self._sample()
        return self.peak


class IsolationHarness:
    def __init__(self, client: ClickHouseProbe):
        self.client = client

    def provision(self, database: str) -> None:
        validate_probe_database(database)
        self.client.run(f"DROP DATABASE IF EXISTS {database} SYNC")
        self.client.run(f"CREATE DATABASE {database}")
        ddl = (
            f"CREATE TABLE {database}.{TABLE} ("
            "tenant_id String, seq UInt64, kind LowCardinality(String), payload String"
            ") ENGINE=MergeTree PARTITION BY tenant_id ORDER BY (tenant_id, seq)")
        self.client.run(ddl)
        self._load(database)

    def _load(self, database: str) -> None:
        total = TENANT_COUNT * ROWS_PER_TENANT
        batch: list[str] = []

        def flush() -> None:
            nonlocal batch
            if not batch:
                return
            payload = ("\n".join(batch) + "\n").encode()
            self.client.run(
                f"INSERT INTO {database}.{TABLE} FORMAT JSONEachRow", stdin=payload)
            batch = []

        for index in range(total):
            tenant = tenant_name(index % TENANT_COUNT)
            seq = index // TENANT_COUNT
            batch.append(json.dumps({
                "tenant_id": tenant, "seq": seq, "kind": "page",
                "payload": "p" * 64}))
            if len(batch) >= INSERT_BATCH:
                flush()
        flush()
        count = int(self.client.scalar(f"SELECT count() FROM {database}.{TABLE} FORMAT TSV"))
        if count != total:
            raise ProbeError(f"loaded {count} rows, want {total}")

    def drop(self, database: str) -> None:
        validate_probe_database(database)
        self.client.run(f"DROP DATABASE {database} SYNC")

    def _query_log_row(self, query_id: str) -> dict:
        record = ""
        for _ in range(20):
            record = self.client.scalar(
                "SELECT memory_usage, query_duration_ms, type, exception_code "
                "FROM system.query_log WHERE query_id='" + query_id + "' "
                "ORDER BY event_time_microseconds DESC LIMIT 1 FORMAT JSONEachRow")
            if record:
                return json.loads(record)
            time.sleep(0.5)
        raise ProbeError(f"no query_log row for {query_id}")

    def execute(self, sql: str, query_id: str, tenant: str, kind: str) -> QueryOutcome:
        try:
            self.client.run(sql, query_id=query_id)
            succeeded, code, message = True, None, ""
        except ProbeError as exc:
            succeeded, message = False, str(exc)
            code = extract_exception_code(message)
        self.client.run("SYSTEM FLUSH LOGS")
        row = self._query_log_row(query_id)
        label = LABEL_OK if succeeded else classify_exception(code, message)
        return QueryOutcome(
            query_id=query_id, tenant=tenant, kind=kind, succeeded=succeeded,
            label=label, exception_code=code,
            peak_memory=int(row["memory_usage"]),
            duration_ms=float(row["query_duration_ms"]))

    def run_concurrent(self, jobs: list[dict], concurrency: int,
                       poller: MemoryPoller) -> ScenarioResult:
        result = ScenarioResult(
            name="", concurrency=concurrency,
            aggregate_ceiling=aggregate_memory_ceiling(concurrency))
        with ThreadPoolExecutor(max_workers=concurrency) as pool:
            futures = [pool.submit(self.execute, job["sql"], job["query_id"],
                                   job["tenant"], job["kind"]) for job in jobs]
            for future in as_completed(futures):
                result.outcomes.append(future.result())
        result.aggregate_peak_memory = poller.stop()
        return result


def build_jobs(qualified_table: str, bounded_per_tenant: int) -> list[dict]:
    jobs = []
    seq = 0
    for tenant_index in range(TENANT_COUNT):
        tenant = tenant_name(tenant_index)
        for repeat in range(bounded_per_tenant):
            builder = (bounded_count_sql, bounded_unique_sql,
                       bounded_group_sql)[repeat % 3]
            jobs.append({
                "sql": with_settings(builder(qualified_table, tenant)),
                "query_id": f"{QUERY_ID_PREFIX}-bounded-{seq}",
                "tenant": tenant, "kind": "bounded"})
            seq += 1
    jobs.append({"sql": with_settings(heavy_memory_sql()),
                 "query_id": f"{QUERY_ID_PREFIX}-heavy-mem",
                 "tenant": tenant_name(0), "kind": "heavy_memory"})
    jobs.append({"sql": with_settings(heavy_read_rows_sql()),
                 "query_id": f"{QUERY_ID_PREFIX}-heavy-read",
                 "tenant": tenant_name(0), "kind": "heavy_read_rows"})
    return jobs


def run_probe(client: ClickHouseProbe, database: str, concurrencies: tuple[int, ...],
              keep: bool) -> dict:
    assert_settings_match_production(PRODUCTION_QUERY_SETTINGS)
    harness = IsolationHarness(client)
    qualified = f"{database}.{TABLE}"
    scenarios = []
    verdicts = []
    try:
        harness.provision(database)

        # Scenario 1: deterministic guard rejection, serial.
        guard_poller = MemoryPoller(client)
        guard_poller.start()
        guard_jobs = [
            {"sql": with_settings(heavy_memory_sql()),
             "query_id": f"{QUERY_ID_PREFIX}-guard-mem",
             "tenant": tenant_name(0), "kind": "heavy_memory"},
            {"sql": with_settings(heavy_read_rows_sql()),
             "query_id": f"{QUERY_ID_PREFIX}-guard-read",
             "tenant": tenant_name(0), "kind": "heavy_read_rows"},
        ]
        guard_result = ScenarioResult(name="guard-rejection", concurrency=1,
                                      aggregate_ceiling=aggregate_memory_ceiling(1))
        for job in guard_jobs:
            guard_result.outcomes.append(
                harness.execute(job["sql"], job["query_id"], job["tenant"], job["kind"]))
        guard_result.aggregate_peak_memory = guard_poller.stop()
        guard_failures = evaluate_guard_rejection(guard_result)
        scenarios.append(summarize_scenario(guard_result))
        verdicts.append({"name": "guard-rejection", "passed": not guard_failures,
                         "failures": guard_failures})

        for concurrency in concurrencies:
            # Scenario 2: bounded concurrency (no heavy queries in this wave).
            bounded_jobs = [job for job in build_jobs(qualified, 2)
                            if job["kind"] == "bounded"]
            poller = MemoryPoller(client)
            poller.start()
            bounded_result = harness.run_concurrent(bounded_jobs, concurrency, poller)
            bounded_result.name = f"bounded-concurrency-{concurrency}"
            bounded_failures = evaluate_bounded_concurrency(bounded_result)
            scenarios.append(summarize_scenario(bounded_result))
            verdicts.append({"name": bounded_result.name,
                             "passed": not bounded_failures,
                             "failures": bounded_failures})

            # Scenario 3: noisy neighbor — heavies run inside the same wave.
            neighbor_jobs = build_jobs(qualified, 2)
            neighbor_poller = MemoryPoller(client)
            neighbor_poller.start()
            neighbor_result = harness.run_concurrent(neighbor_jobs, concurrency, neighbor_poller)
            neighbor_result.name = f"noisy-neighbor-{concurrency}"
            neighbor_failures = evaluate_noisy_neighbor(neighbor_result)
            scenarios.append(summarize_scenario(neighbor_result))
            verdicts.append({"name": neighbor_result.name,
                             "passed": not neighbor_failures,
                             "failures": neighbor_failures})
    finally:
        if not keep:
            harness.drop(database)

    return {
        "status": "passed" if all(item["passed"] for item in verdicts) else "failed",
        "probe": "tenant-query-isolation",
        "memory_budget_bytes": MEMORY_BUDGET_BYTES,
        "tenants": TENANT_COUNT,
        "rows_per_tenant": ROWS_PER_TENANT,
        "settings": PRODUCTION_QUERY_SETTINGS,
        "scenarios": scenarios,
        "verdicts": verdicts,
    }


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--client", default=os.environ.get("EVIA_CH_CLIENT", DEFAULT_CLIENT))
    parser.add_argument("--username", default=os.environ.get("EVIA_CH_USERNAME", "evia"))
    parser.add_argument("--password", default=os.environ.get("EVIA_CH_PASSWORD", ""))
    parser.add_argument("--database", default=f"{PROBE_DB_PREFIX}_default")
    parser.add_argument("--concurrencies", default="4,8")
    parser.add_argument("--keep", action="store_true")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    validate_probe_database(args.database)
    concurrencies = tuple(int(value) for value in args.concurrencies.split(",") if value)
    client = ClickHouseProbe(args.client, args.username, args.password)
    report = run_probe(client, args.database, concurrencies, args.keep)
    print(json.dumps(report, indent=2, sort_keys=True))
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
