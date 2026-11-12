#!/usr/bin/env python3
"""Physical-read gate probe for successor property storage layouts.

The Map(String, String) token layout failed the physical-read gate recorded in
docs/verification/property-layout-reads-2026-11-10.md: it reads more bytes and
marks and needs substantially more peak memory than the authoritative document
column. The property layout plan requires every successor to pass the *same*
dataset and metric gate before any production rollout.

This module is an isolated experiment harness, not production code: it creates
a throwaway ClickHouse database, loads identical wide-document rows into three
layouts, runs semantically identical queries and compares the deterministic
physical-read metrics from system.query_log:

* legacy    - the authoritative properties_json String column (baseline).
* token map - property_tokens Map(String, String), the parked layout.
* dedicated - a bounded set of hot keys materialized as typed columns, the
              first successor candidate named in the layout plan.
* dedicated_token - the same hot keys materialized as String columns holding the
              exact JSON token text, read through the same synthetic one-field
              document the production reader uses (lossless Option A in the
              layout plan); the sub-gate against the typed column decides the
              lexical-fidelity trade-off.

A candidate passes only when read_bytes, SelectedMarks and peak memory are all
no worse than the document baseline within an explicit measurement tolerance.
The verdict is advisory evidence for a design decision; nothing here changes
the writer, reader or any migration.
"""
from __future__ import annotations

import argparse
import dataclasses
import json
import os
import re
import shlex
import subprocess
import sys
import time
from pathlib import Path

STRING_KEY_COUNT = 24
STRING_VALUE_WIDTH = 100
NUMERIC_KEY = "metric_07"
NUMERIC_DOMAIN = 100
HOT_STRING_KEY = "segment_03"
DEFAULT_TOLERANCE = 0.02
DATABASE_PATTERN = re.compile(r"^evia_probe_[a-z0-9_]{3,40}$")
IDENTIFIER_PATTERN = re.compile(r"^[a-z_][a-z0-9_]{0,62}$")
GATE_METRICS = ("read_bytes", "selected_marks", "peak_memory")
# Continuous byte/memory metrics allow only 2% measurement noise. Mark counts
# are discrete granule integers, so a one-granule alignment difference is
# quantization rather than a structural regression; the absolute allowance
# still rejects layouts that touch twice as many granules (the parked Map).
CONTINUOUS_TOLERANCE = 0.02
MARK_ABSOLUTE_ALLOWANCE = 2
MARK_RATIO_BOUND = 1.10
DEFAULT_CLIENT = "docker exec evia-pipeline-dev-clickhouse-1 clickhouse-client"


class ProbeError(RuntimeError):
    """All harness failures normalize to this type."""


# ---------------------------------------------------------------------------
# Pure data generation and SQL construction (unit-tested without ClickHouse)
# ---------------------------------------------------------------------------

def _fixed_width_value(key: str, index: int) -> str:
    """Deterministic ~100 byte value; repeated values model the gate profile."""
    base = f"{key}-row-{index:08d}"
    if len(base) >= STRING_VALUE_WIDTH:
        return base[:STRING_VALUE_WIDTH]
    return base + "x" * (STRING_VALUE_WIDTH - len(base))


def generate_row(index: int) -> dict:
    """One wide document matching the 2026-11-10 gate profile."""
    properties = {
        f"segment_{slot:02d}": _fixed_width_value(f"segment_{slot:02d}", index)
        for slot in range(STRING_KEY_COUNT)
    }
    properties[NUMERIC_KEY] = index % NUMERIC_DOMAIN
    return {
        "id": index,
        "properties_json": json.dumps(properties, separators=(",", ":")),
        "metric_value": index % NUMERIC_DOMAIN,
        "segment_value": properties[HOT_STRING_KEY],
    }


def render_json_each_row(row: dict) -> str:
    return json.dumps(row, separators=(",", ":"), ensure_ascii=False)


def encode_key(key: str) -> str:
    """Mirror internal/propertylayout.EncodeKey: p_ plus uppercase UTF-8 hex."""
    return "p_" + key.encode("utf-8").hex().upper()


def derive_tokens(properties_json: str) -> dict[str, str]:
    """Derive exact JSON tokens, matching the Go propertylayout deriver.

    Numeric and boolean tokens stay bare (``42``, ``true``); string, array and
    object tokens keep their full JSON spelling (``"Home"``, ``["a"]``) so no
    lexical normalization happens. The probe only generates int/string values,
    but the mapping stays generic for nested containers.
    """
    document = json.loads(properties_json)
    if not isinstance(document, dict):
        raise ProbeError("properties document must be a JSON object")
    tokens: dict[str, str] = {}
    for key, value in document.items():
        if isinstance(value, str):
            tokens[encode_key(key)] = json.dumps(value, ensure_ascii=False)
        elif isinstance(value, bool):
            tokens[encode_key(key)] = "true" if value else "false"
        elif isinstance(value, int):
            tokens[encode_key(key)] = str(value)
        else:
            tokens[encode_key(key)] = json.dumps(value, separators=(",", ":"), ensure_ascii=False)
    return tokens


def validate_identifier(name: str) -> None:
    if not IDENTIFIER_PATTERN.fullmatch(name):
        raise ProbeError(f"unsafe SQL identifier: {name!r}")


def validate_database(name: str) -> None:
    if not DATABASE_PATTERN.fullmatch(name):
        raise ProbeError(
            f"probe database must match {DATABASE_PATTERN.pattern} so it cannot target a real database"
        )


def legacy_ddl(table: str) -> str:
    validate_identifier(table)
    return (
        f"CREATE TABLE {table} ("
        "id UInt64, "
        "properties_json String"
        ") ENGINE=MergeTree ORDER BY id"
    )


def map_ddl(table: str) -> str:
    validate_identifier(table)
    return (
        f"CREATE TABLE {table} ("
        "id UInt64, "
        "properties_json String, "
        "property_layout_version UInt8 DEFAULT 1, "
        "property_tokens Map(String, String)"
        ") ENGINE=MergeTree ORDER BY id"
    )


def dedicated_ddl(table: str, hot_string_key: str, numeric_key: str) -> str:
    for name in (table, hot_string_key, numeric_key):
        validate_identifier(name)
    return (
        f"CREATE TABLE {table} ("
        "id UInt64, "
        "properties_json String, "
        f"{hot_string_key} String, "
        f"{numeric_key} Int64"
        ") ENGINE=MergeTree ORDER BY id"
    )


def dedicated_token_ddl(table: str, hot_string_key: str, numeric_key: str) -> str:
    """String columns holding exact JSON tokens (lossless Option A).

    The numeric token stays bare text (``49``) and the string token keeps its
    JSON spelling (``"Home"``); readers wrap either in a synthetic one-field
    document so the existing JSON extraction semantics apply unchanged.
    """
    for name in (table, hot_string_key, numeric_key):
        validate_identifier(name)
    return (
        f"CREATE TABLE {table} ("
        "id UInt64, "
        "properties_json String, "
        f"{hot_string_key}_token String, "
        f"{numeric_key}_token String"
        ") ENGINE=MergeTree ORDER BY id"
    )


def synthetic_document(token_column: str) -> str:
    """Mirror the planner's one-field document built from an exact token."""
    validate_identifier(token_column)
    return f"concat('{{\"f\":',{token_column},'}}')"


def numeric_filter_queries(
    hot_numeric_column: str | None = None,
    token_numeric_column: str | None = None,
) -> dict[str, str]:
    """Semantically identical count queries; each returns the same row count."""
    if hot_numeric_column is not None:
        validate_identifier(hot_numeric_column)
        dedicated = f"SELECT count() FROM {{table}} WHERE {hot_numeric_column} > 50"
    else:
        dedicated = None
    token_query = ""
    if token_numeric_column is not None:
        token_doc = synthetic_document(token_numeric_column)
        token_query = f"SELECT count() FROM {{table}} WHERE JSONExtractInt({token_doc}, 'f') > 50"
    return {
        "legacy": "SELECT count() FROM {table} WHERE JSONExtractInt(properties_json, 'metric_07') > 50",
        "map": (
            "SELECT count() FROM {table} WHERE property_layout_version = 1 "
            "AND toInt64OrZero(property_tokens['p_6D65747269635F3037']) > 50"
        ),
        "dedicated": dedicated or "",
        "dedicated_token": token_query,
    }


def string_group_queries(
    hot_string_column: str | None = None,
    token_string_column: str | None = None,
) -> dict[str, str]:
    if hot_string_column is not None:
        validate_identifier(hot_string_column)
        dedicated = f"SELECT count() FROM {{table}} GROUP BY {hot_string_column}"
    else:
        dedicated = None
    token_query = ""
    if token_string_column is not None:
        token_doc = synthetic_document(token_string_column)
        token_query = (
            "SELECT count() FROM (SELECT count() FROM {table} "
            f"GROUP BY JSONExtractString({token_doc}, 'f'))"
        )
    # Wrap the grouping in a count subquery so every layout yields one scalar
    # (the number of distinct groups), which is directly comparable.
    return {
        "legacy": "SELECT count() FROM (SELECT count() FROM {table} GROUP BY JSONExtractString(properties_json, 'segment_03'))",
        "map": "SELECT count() FROM (SELECT count() FROM {table} GROUP BY property_tokens['p_7365676D656E745F3033'])",
        "dedicated": "SELECT count() FROM (SELECT count() FROM {table} GROUP BY segment_03)",
        "dedicated_token": token_query,
    }


@dataclasses.dataclass(frozen=True)
class Measurement:
    layout: str
    workload: str
    read_bytes: int
    selected_marks: int
    peak_memory: int
    duration_ms: float
    result_check: str

    def as_dict(self) -> dict:
        return dataclasses.asdict(self)


def ratio(candidate: int, baseline: int) -> float:
    if baseline == 0:
        return 0.0 if candidate == 0 else float("inf")
    return candidate / baseline


def verdict(baseline: Measurement, candidate: Measurement, tolerance: float = CONTINUOUS_TOLERANCE) -> dict:
    """Gate decision: every physical metric must stay within its bound.

    Continuous metrics use a relative tolerance; SelectedMarks additionally
    accepts a small absolute granule-count delta while capping the ratio, so
    alignment noise passes but a structurally wider read does not.
    """
    if baseline.workload != candidate.workload:
        raise ProbeError("cannot compare measurements from different workloads")
    comparisons = {}
    passed = True
    for metric in GATE_METRICS:
        base = getattr(baseline, metric)
        value = getattr(candidate, metric)
        observed = ratio(value, base)
        if metric == "selected_marks":
            ok = (value - base) <= MARK_ABSOLUTE_ALLOWANCE and observed <= MARK_RATIO_BOUND
            bound = f"abs<={MARK_ABSOLUTE_ALLOWANCE} marks and ratio<={MARK_RATIO_BOUND}"
        else:
            ok = value <= int(base * (1 + tolerance)) if base else value == 0
            bound = f"relative<={tolerance}"
        comparisons[metric] = {
            "baseline": base,
            "candidate": value,
            "ratio": round(observed, 4),
            "bound": bound,
            "within_tolerance": ok,
        }
        passed = passed and ok
    return {
        "layout": candidate.layout,
        "workload": candidate.workload,
        "continuous_tolerance": tolerance,
        "mark_absolute_allowance": MARK_ABSOLUTE_ALLOWANCE,
        "mark_ratio_bound": MARK_RATIO_BOUND,
        "passed": passed,
        "metrics": comparisons,
    }


# ---------------------------------------------------------------------------
# ClickHouse execution
# ---------------------------------------------------------------------------

class ClickHouseProbe:
    def __init__(self, client_prefix: str, username: str, password: str, timeout: int = 600):
        self.argv_prefix = shlex.split(client_prefix)
        self.username = username
        self.password = password
        self.timeout = timeout

    def _argv(self, sql: str, interactive: bool = False, query_id: str | None = None) -> list[str]:
        argv = [
            "docker",
            "exec",
            *(["-i"] if interactive else []),
            "-e",
            "CLICKHOUSE_PASSWORD",
            *self.argv_prefix[2:],
            "--user",
            self.username,
        ]
        if query_id:
            argv += ["--query_id", query_id]
        argv += ["--query", sql]
        return argv

    def run(self, sql: str, stdin: bytes | None = None) -> str:
        env = dict(os.environ)
        env["CLICKHOUSE_PASSWORD"] = self.password
        try:
            completed = subprocess.run(
                self._argv(sql, interactive=stdin is not None, query_id=getattr(self, "_next_query_id", None)),
                input=stdin,
                env=env,
                timeout=self.timeout,
                check=False,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
        except subprocess.TimeoutExpired as error:
            raise ProbeError(f"clickhouse query timed out: {sql[:80]}") from error
        if completed.returncode != 0:
            raise ProbeError(f"clickhouse query failed: {completed.stderr.decode().strip()[:300]}")
        return completed.stdout.decode()

    def scalar(self, sql: str) -> str:
        return self.run(sql).strip()


def build_probe(client: ClickHouseProbe, database: str, rows: int, repeats: int) -> dict:
    validate_database(database)
    tables = {
        "legacy": "probe_legacy",
        "map": "probe_map",
        "dedicated": "probe_dedicated",
        "dedicated_token": "probe_dedicated_token",
    }
    for table in tables.values():
        validate_identifier(table)

    client.run(f"DROP DATABASE IF EXISTS {database} SYNC")
    client.run(f"CREATE DATABASE {database}")
    client.run(legacy_ddl(tables["legacy"]).replace(
        f"CREATE TABLE {tables['legacy']}", f"CREATE TABLE {database}.{tables['legacy']}"))
    client.run(map_ddl(tables["map"]).replace(
        f"CREATE TABLE {tables['map']}", f"CREATE TABLE {database}.{tables['map']}"))
    client.run(dedicated_ddl(tables["dedicated"], HOT_STRING_KEY, NUMERIC_KEY).replace(
        f"CREATE TABLE {tables['dedicated']}", f"CREATE TABLE {database}.{tables['dedicated']}"))
    client.run(dedicated_token_ddl(tables["dedicated_token"], HOT_STRING_KEY, NUMERIC_KEY).replace(
        f"CREATE TABLE {tables['dedicated_token']}",
        f"CREATE TABLE {database}.{tables['dedicated_token']}"))

    raw_rows = [generate_row(i) for i in range(rows)]
    payloads = {
        "legacy": [
            {"id": row["id"], "properties_json": row["properties_json"]} for row in raw_rows
        ],
        "map": [
            {
                "id": row["id"],
                "properties_json": row["properties_json"],
                "property_layout_version": 1,
                "property_tokens": derive_tokens(row["properties_json"]),
            }
            for row in raw_rows
        ],
        "dedicated": [
            {
                "id": row["id"],
                "properties_json": row["properties_json"],
                HOT_STRING_KEY: row["segment_value"],
                NUMERIC_KEY: row["metric_value"],
            }
            for row in raw_rows
        ],
    }
    token_payloads = []
    for row in raw_rows:
        tokens = derive_tokens(row["properties_json"])
        token_payloads.append(
            {
                "id": row["id"],
                "properties_json": row["properties_json"],
                f"{HOT_STRING_KEY}_token": tokens[encode_key(HOT_STRING_KEY)],
                f"{NUMERIC_KEY}_token": tokens[encode_key(NUMERIC_KEY)],
            }
        )
    payloads["dedicated_token"] = token_payloads
    for layout, table in tables.items():
        payload = "\n".join(render_json_each_row(row) for row in payloads[layout]).encode()
        client.run(
            f"INSERT INTO {database}.{table} FORMAT JSONEachRow",
            stdin=payload,
        )
        client.run(f"OPTIMIZE TABLE {database}.{table} FINAL")

    # Semantic equivalence check before any physical measurement.
    expected_filter = str(sum(1 for i in range(rows) if i % NUMERIC_DOMAIN > 50))
    workloads = {
        "numeric_filter": numeric_filter_queries(NUMERIC_KEY, f"{NUMERIC_KEY}_token"),
        "string_group": string_group_queries(HOT_STRING_KEY, f"{HOT_STRING_KEY}_token"),
    }
    measurements: list[Measurement] = []
    verdicts: list[dict] = []
    for workload_name, queries in workloads.items():
        per_layout: dict[str, Measurement] = {}
        for layout, table in tables.items():
            sql_template = queries[layout]
            if not sql_template:
                continue
            samples = []
            for repeat in range(repeats):
                query_id = f"evia-probe-{workload_name}-{layout}-{repeat}"
                client.run("SYSTEM DROP MARK CACHE")
                client._next_query_id = query_id
                try:
                    client.run(
                        sql_template.replace("{table}", f"{database}.{table}")
                        + " SETTINGS max_threads=2"
                    )
                finally:
                    client._next_query_id = None
                # query_log flushes asynchronously; force the flush in this
                # isolated probe database, then poll with a bounded deadline
                # rather than assuming immediate visibility.
                client.run("SYSTEM FLUSH LOGS")
                row = ""
                for _ in range(20):
                    row = client.scalar(
                        "SELECT "
                        "read_bytes, "
                        "ProfileEvents['SelectedMarks'] AS selected_marks, "
                        "memory_usage AS peak_memory, "
                        "query_duration_ms "
                        "FROM system.query_log "
                        f"WHERE query_id='{query_id}' AND type='QueryFinish' "
                        "ORDER BY event_time_microseconds DESC LIMIT 1 FORMAT JSONEachRow"
                    )
                    if row:
                        break
                    time.sleep(0.5)
                if not row:
                    raise ProbeError(f"no QueryFinish row for {query_id}")
                samples.append(json.loads(row))
            stable = samples[-1]
            result_check = client.scalar(
                sql_template.replace("{table}", f"{database}.{tables['legacy']}")
                if layout == "legacy"
                else sql_template.replace("{table}", f"{database}.{table}")
            ).strip()
            per_layout[layout] = Measurement(
                layout=layout,
                workload=workload_name,
                read_bytes=int(stable["read_bytes"]),
                selected_marks=int(stable["selected_marks"]),
                peak_memory=int(stable["peak_memory"]),
                duration_ms=float(stable["query_duration_ms"]),
                result_check=result_check,
            )
        result_values = {measurement.result_check for measurement in per_layout.values()}
        if len(result_values) != 1:
            raise ProbeError(
                f"semantic mismatch across layouts for {workload_name}: {result_values}"
            )
        if workload_name == "numeric_filter" and next(iter(result_values)) != expected_filter:
            raise ProbeError(
                f"numeric filter count {next(iter(result_values))} != {expected_filter}"
            )
        baseline = per_layout["legacy"]
        for layout in ("map", "dedicated", "dedicated_token"):
            if layout in per_layout:
                verdicts.append(verdict(baseline, per_layout[layout]))
        measurements.extend(per_layout.values())

    return {
        "database": database,
        "rows": rows,
        "repeats": repeats,
        "measurements": [item.as_dict() for item in measurements],
        "verdicts": verdicts,
    }


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Property layout physical-read gate probe")
    sub = parser.add_subparsers(dest="command", required=True)
    run = sub.add_parser("run", help="create an isolated probe database and measure all layouts")
    run.add_argument("--database", required=True)
    run.add_argument("--rows", type=int, default=100_000)
    run.add_argument("--repeats", type=int, default=5)
    run.add_argument("--client", default=os.environ.get("EVIA_CH_CLIENT", DEFAULT_CLIENT))
    run.add_argument("--username", default=os.environ.get("EVIA_CH_USERNAME", "evia"))
    run.add_argument("--password", default=os.environ.get("EVIA_CH_PASSWORD", ""))
    run.add_argument("--keep", action="store_true", help="keep the probe database for inspection")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if args.command == "run":
        if args.rows < 1000 or args.rows > 1_000_000:
            print("rows must be between 1,000 and 1,000,000", file=sys.stderr)
            return 1
        if args.repeats < 2 or args.repeats > 20:
            print("repeats must be between 2 and 20", file=sys.stderr)
            return 1
        client = ClickHouseProbe(args.client, args.username, args.password)
        try:
            evidence = build_probe(client, args.database, args.rows, args.repeats)
        finally:
            if not args.keep:
                client.run(f"DROP DATABASE IF EXISTS {args.database} SYNC")
        print(json.dumps(evidence, indent=2, sort_keys=True))
        return 0
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
