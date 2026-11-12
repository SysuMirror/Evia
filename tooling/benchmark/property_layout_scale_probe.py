#!/usr/bin/env python3
"""G3 million-row physical-read matrix for property storage layouts.

G1/G2 proved the dedicated-column gate at 100k rows. G3 answers the capacity
question behind the parked map layout: at million-row scale and under
concurrent load, do dedicated hot-key columns keep the property filter/group
workloads (the 182 failures in the 514/728 matrix) inside the production
per-query memory budget, without regressing non-property baselines?

The harness is scale-agnostic and streams its load in batches so a million
rows never have to reside in Python memory at once. Every layout shares one
deterministic wide-document generator, and every query shape is a pure SQL
builder so it can be unit-tested without ClickHouse. Failures (including
resource exhaustion) are captured as structured status rather than raised,
because a query that cannot run under the budget is itself the evidence.

This is isolated experiment tooling: it only touches databases matching
evia_probe_* and the operator owns dropping them.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass

from property_layout_probe import (
    DEFAULT_CLIENT,
    HOT_STRING_KEY,
    NUMERIC_KEY,
    ClickHouseProbe,
    ProbeError,
    dedicated_ddl,
    dedicated_token_ddl,
    derive_tokens,
    encode_key,
    generate_row,
    legacy_ddl,
    map_ddl,
    render_json_each_row,
    synthetic_document,
    validate_database,
    validate_identifier,
)

# Production per-query memory ceiling enforced by the analysis engine
# (max_memory_usage in the ClickHouse request settings).
MEMORY_BUDGET_BYTES = 512 * 1024 * 1024
# Keep each JSONEachRow batch well under the engine's 16 MiB body limit while
# amortizing insert round trips at million-row scale.
INSERT_BATCH = 25_000

SCALE_LAYOUTS = ("legacy", "map", "dedicated", "dedicated_token")
MATRIX_SHAPES = ("count", "unique", "numeric_filter", "string_group")


# ---------------------------------------------------------------------------
# Streaming provisioning
# ---------------------------------------------------------------------------

def scale_tables() -> dict[str, str]:
    tables = {
        "legacy": "scale_legacy",
        "map": "scale_map",
        "dedicated": "scale_dedicated",
        "dedicated_token": "scale_dedicated_token",
    }
    for table in tables.values():
        validate_identifier(table)
    return tables


def _validate_qualified(name: str) -> None:
    parts = name.split(".")
    if not parts or any(not part for part in parts):
        raise ProbeError(f"unsafe qualified identifier {name!r}")
    for part in parts:
        validate_identifier(part)


def scale_ddl(layout: str, table: str) -> str:
    if layout == "legacy":
        return legacy_ddl(table)
    if layout == "map":
        return map_ddl(table)
    if layout == "dedicated":
        return dedicated_ddl(table, HOT_STRING_KEY, NUMERIC_KEY)
    if layout == "dedicated_token":
        return dedicated_token_ddl(table, HOT_STRING_KEY, NUMERIC_KEY)
    raise ProbeError(f"unknown layout {layout!r}")


def _row_payload(layout: str, row: dict) -> dict:
    if layout == "legacy":
        return {"id": row["id"], "properties_json": row["properties_json"]}
    if layout == "map":
        return {
            "id": row["id"],
            "properties_json": row["properties_json"],
            "property_layout_version": 1,
            "property_tokens": derive_tokens(row["properties_json"]),
        }
    if layout == "dedicated":
        return {
            "id": row["id"],
            "properties_json": row["properties_json"],
            HOT_STRING_KEY: row["segment_value"],
            NUMERIC_KEY: row["metric_value"],
        }
    tokens = derive_tokens(row["properties_json"])
    return {
        "id": row["id"],
        "properties_json": row["properties_json"],
        f"{HOT_STRING_KEY}_token": tokens[encode_key(HOT_STRING_KEY)],
        f"{NUMERIC_KEY}_token": tokens[encode_key(NUMERIC_KEY)],
    }


def stream_load(client: ClickHouseProbe, database: str, table: str, layout: str, rows: int) -> int:
    """Generate and insert `rows` in bounded batches without holding them all."""
    written = 0
    for start in range(0, rows, INSERT_BATCH):
        end = min(start + INSERT_BATCH, rows)
        lines = [
            render_json_each_row(_row_payload(layout, generate_row(index)))
            for index in range(start, end)
        ]
        client.run(
            f"INSERT INTO {database}.{table} FORMAT JSONEachRow",
            stdin=("\n".join(lines) + "\n").encode(),
        )
        written += end - start
    client.run(f"OPTIMIZE TABLE {database}.{table} FINAL")
    return written


def provision_scale_tables(client: ClickHouseProbe, database: str, rows: int) -> dict[str, str]:
    validate_database(database)
    tables = scale_tables()
    client.run(f"DROP DATABASE IF EXISTS {database} SYNC")
    client.run(f"CREATE DATABASE {database}")
    for layout, table in tables.items():
        client.run(
            scale_ddl(layout, table).replace(
                f"CREATE TABLE {table}", f"CREATE TABLE {database}.{table}"
            )
        )
    for layout, table in tables.items():
        loaded = stream_load(client, database, table, layout, rows)
        if loaded != rows:
            raise ProbeError(f"{layout} loaded {loaded} rows, want {rows}")
    return tables


# ---------------------------------------------------------------------------
# Pure matrix query builders
# ---------------------------------------------------------------------------

def matrix_query(layout: str, table: str, shape: str) -> str:
    _validate_qualified(table)
    qualified = table
    if shape == "count":
        return f"SELECT count() FROM {qualified}"
    if shape == "unique":
        # UV baseline: distinct id cardinality, identical across layouts.
        return f"SELECT uniqExact(id) FROM {qualified}"
    if shape == "numeric_filter":
        return _numeric_filter(layout, qualified)
    if shape == "string_group":
        return _string_group(layout, qualified)
    raise ProbeError(f"unknown matrix shape {shape!r}")


def _numeric_filter(layout: str, table: str) -> str:
    if layout == "legacy":
        return f"SELECT count() FROM {table} WHERE JSONExtractInt(properties_json, '{NUMERIC_KEY}') > 50"
    if layout == "map":
        key = encode_key(NUMERIC_KEY)
        return (
            f"SELECT count() FROM {table} WHERE "
            f"toInt64OrZero(property_tokens['{key}']) > 50"
        )
    if layout == "dedicated":
        return f"SELECT count() FROM {table} WHERE {NUMERIC_KEY} > 50"
    token_column = f"{NUMERIC_KEY}_token"
    return (
        f"SELECT count() FROM {table} WHERE "
        f"JSONExtractInt({synthetic_document(token_column)}, 'f') > 50"
    )


def _string_group(layout: str, table: str) -> str:
    # Wrap in a count subquery so every layout yields one scalar comparable
    # across engines; the grouping still materializes distinct string values.
    if layout == "legacy":
        inner = f"SELECT JSONExtractString(properties_json, '{HOT_STRING_KEY}') AS g FROM {table}"
    elif layout == "map":
        key = encode_key(HOT_STRING_KEY)
        inner = f"SELECT property_tokens['{key}'] AS g FROM {table}"
    elif layout == "dedicated":
        inner = f"SELECT {HOT_STRING_KEY} AS g FROM {table}"
    else:
        token_column = f"{HOT_STRING_KEY}_token"
        inner = (
            f"SELECT JSONExtractString({synthetic_document(token_column)}, 'f') AS g "
            f"FROM {table}"
        )
    return f"SELECT count() FROM ({inner} GROUP BY g)"


# ---------------------------------------------------------------------------
# Concurrent execution with soft failure capture
# ---------------------------------------------------------------------------

@dataclass
class QueryOutcome:
    shape: str
    layout: str
    worker: int
    succeeded: bool
    error: str
    read_bytes: int
    selected_marks: int
    peak_memory: int
    duration_ms: float
    result_check: str


def _query_log_row(client: ClickHouseProbe, query_id: str) -> dict:
    record = ""
    for _ in range(20):
        record = client.scalar(
            "SELECT read_bytes, ProfileEvents['SelectedMarks'] AS selected_marks, "
            "memory_usage AS peak_memory, query_duration_ms, type "
            "FROM system.query_log WHERE query_id='" + query_id + "' "
            "ORDER BY event_time_microseconds DESC LIMIT 1 FORMAT JSONEachRow"
        )
        if record:
            return json.loads(record)
        time.sleep(0.5)
    raise ProbeError(f"no query_log row for {query_id}")


def run_matrix(
    client: ClickHouseProbe,
    tables: dict[str, str],
    concurrency: int,
    repeats: int,
) -> list[QueryOutcome]:
    if concurrency < 1 or concurrency > 16:
        raise ProbeError("concurrency must be between 1 and 16")
    if repeats < 1 or repeats > 30:
        raise ProbeError("repeats must be between 1 and 30")

    jobs: list[tuple[str, str, int]] = []
    for shape in MATRIX_SHAPES:
        for layout in SCALE_LAYOUTS:
            for repeat in range(repeats):
                jobs.append((shape, layout, repeat))

    outcomes: list[QueryOutcome] = []

    def execute(shape: str, layout: str, repeat: int) -> QueryOutcome:
        sql = matrix_query(layout, tables[layout], shape) + " SETTINGS max_threads=2"
        query_id = f"evia-g3-{shape}-{layout}-{repeat}"
        client.run("SYSTEM DROP MARK CACHE")
        try:
            client._next_query_id = query_id
            try:
                client.run(sql)
                error = ""
                succeeded = True
            finally:
                client._next_query_id = None
        except ProbeError as exc:
            error = str(exc)
            succeeded = False
        client.run("SYSTEM FLUSH LOGS")
        row = _query_log_row(client, query_id)
        result_check = ""
        if succeeded and row.get("type") == "QueryFinish":
            result_check = client.scalar(sql + " FORMAT TSV").strip()
        elif succeeded:
            succeeded = False
            error = f"query did not finish: {row.get('type')}"
        return QueryOutcome(
            shape=shape,
            layout=layout,
            worker=repeat,
            succeeded=succeeded,
            error=error,
            read_bytes=int(row["read_bytes"]),
            selected_marks=int(row["selected_marks"]),
            peak_memory=int(row["peak_memory"]),
            duration_ms=float(row["query_duration_ms"]),
            result_check=result_check,
        )

    with ThreadPoolExecutor(max_workers=concurrency) as pool:
        futures = [pool.submit(execute, *job) for job in jobs]
        for future in as_completed(futures):
            outcomes.append(future.result())
    outcomes.sort(key=lambda item: (item.shape, item.layout, item.worker))
    return outcomes


def summarize(outcomes: list[QueryOutcome]) -> dict:
    summary: dict = {}
    for shape in MATRIX_SHAPES:
        for layout in SCALE_LAYOUTS:
            rows = [o for o in outcomes if o.shape == shape and o.layout == layout]
            attempts = len(rows)
            successes = sum(1 for o in rows if o.succeeded)
            peak = max((o.peak_memory for o in rows), default=0)
            max_bytes = max((o.read_bytes for o in rows), default=0)
            within_budget = peak <= MEMORY_BUDGET_BYTES
            distinct_results = sorted({o.result_check for o in rows if o.succeeded})
            summary[f"{shape}.{layout}"] = {
                "successes": successes,
                "attempts": attempts,
                "peak_memory": peak,
                "max_read_bytes": max_bytes,
                "within_memory_budget": within_budget,
                "distinct_results": distinct_results,
                "errors": sorted({o.error for o in rows if o.error}),
            }
    equivalence = {}
    for shape in MATRIX_SHAPES:
        values = [
            summary[f"{shape}.{layout}"]["distinct_results"]
            for layout in SCALE_LAYOUTS
            if summary[f"{shape}.{layout}"]["successes"] > 0
        ]
        equivalence[shape] = len({tuple(v) for v in values}) == 1
    summary["_cross_layout_equivalence"] = equivalence
    return summary


def build_scale_probe(
    client: ClickHouseProbe, database: str, rows: int, concurrency: int, repeats: int
) -> dict:
    validate_database(database)
    tables = provision_scale_tables(client, database, rows)
    qualified = {layout: f"{database}.{table}" for layout, table in tables.items()}
    outcomes = run_matrix(client, qualified, concurrency, repeats)
    return {
        "gate": "G3 million-row physical-read matrix",
        "memory_budget_bytes": MEMORY_BUDGET_BYTES,
        "database": database,
        "rows": rows,
        "concurrency": concurrency,
        "repeats": repeats,
        "summary": summarize(outcomes),
        "outcomes": [o.__dict__ for o in outcomes],
    }


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="G3 scale physical-read matrix probe")
    sub = parser.add_subparsers(dest="command", required=True)
    run = sub.add_parser("run")
    run.add_argument("--database", required=True)
    run.add_argument("--rows", type=int, default=1_000_000)
    run.add_argument("--concurrency", type=int, default=4)
    run.add_argument("--repeats", type=int, default=3)
    run.add_argument("--client", default=os.environ.get("EVIA_CH_CLIENT", DEFAULT_CLIENT))
    run.add_argument("--username", default=os.environ.get("EVIA_CH_USERNAME", "evia"))
    run.add_argument("--password", default=os.environ.get("EVIA_CH_PASSWORD", ""))
    run.add_argument("--keep", action="store_true")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if args.command != "run":
        return 1
    if not (1000 <= args.rows <= 2_000_000):
        print("rows must be between 1,000 and 2,000,000", file=sys.stderr)
        return 1
    client = ClickHouseProbe(args.client, args.username, args.password)
    try:
        evidence = build_scale_probe(client, args.database, args.rows, args.concurrency, args.repeats)
    finally:
        if not args.keep:
            client.run(f"DROP DATABASE IF EXISTS {args.database} SYNC")
    print(json.dumps(evidence, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
