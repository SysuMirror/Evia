#!/usr/bin/env python3
"""G2 view-layer physical-read probe for property storage layouts.

The raw-read probe (property_layout_probe.py) measures direct table scans.
G2 measures the *view* path that trend queries actually use: an argMin winner
tuple carrying the layout-specific columns, which is exactly where the parked
token map hit the temporary-disk quota (ClickHouse error 243) in the 2026-11-10
investigation.

For every layout this module creates a minimal argMin view over the shared
probe tables, then runs one trend-style aggregation that materializes the
winner tuple and applies a property predicate. Beyond the standard
read_bytes/marks/memory metrics it records query status so a resource-exhaustion
failure is evidence rather than an unhandled crash.

Like the raw probe this is isolated experiment tooling: every database it
touches matches evia_probe_*, it never writes production data and the caller
is expected to drop the database when the run finishes.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
from dataclasses import dataclass

from property_layout_probe import (
    ClickHouseProbe,
    HOT_STRING_KEY,
    NUMERIC_KEY,
    ProbeError,
    encode_key,
    provision_layout_tables,
    validate_database,
    validate_identifier,
    DEFAULT_CLIENT,
)

# Layouts whose view tuple must be compared. Order is fixed for deterministic
# output and evidence comparison.
VIEW_LAYOUTS = ("legacy", "map", "dedicated", "dedicated_token")


def view_tuple_columns(layout: str) -> list[str]:
    """Extra physical columns carried in the argMin tuple after properties_json."""
    if layout == "legacy":
        return []
    if layout == "map":
        # The parked design carries the whole token map through every winner.
        return ["property_tokens"]
    if layout == "dedicated":
        return [NUMERIC_KEY, HOT_STRING_KEY]
    if layout == "dedicated_token":
        return [f"{NUMERIC_KEY}_token", f"{HOT_STRING_KEY}_token"]
    raise ProbeError(f"unknown view layout {layout!r}")


def view_name(layout: str) -> str:
    validate_identifier(layout)
    return f"probe_view_{layout}"


def validate_qualified_identifier(name: str) -> None:
    """Validate a possibly database-qualified identifier (db.table) part by part."""
    parts = name.split(".")
    if not parts or any(not part for part in parts):
        raise ProbeError(f"unsafe qualified identifier {name!r}")
    for part in parts:
        validate_identifier(part)


def view_ddl(layout: str, table: str) -> str:
    """Minimal argMin view mirroring the event_facts winner-tuple shape.

    The weight is tuple(id) because the probe tables only expose id ordering;
    the physical cost under test is tuple width, not the specific weight
    columns, so this keeps the view faithful to the production argMin while
    staying self-contained.
    """
    validate_qualified_identifier(table)
    columns = ["properties_json", *view_tuple_columns(layout)]
    tuple_sql = ",".join(columns)
    return (
        f"CREATE VIEW {view_name(layout)} AS "
        f"SELECT id, argMin(tuple({tuple_sql}), tuple(id)) AS winner FROM {table} "
        "GROUP BY id"
    )


def view_trend_query(layout: str) -> str:
    """Trend-style query that materializes the winner tuple and filters on it.

    Each layout filters the same logical hot numeric property using its own
    storage path, so all four must return the same row count while exposing
    different tuple materialization costs.
    """
    view = view_name(layout)
    if layout == "legacy":
        predicate = "JSONExtractInt(winner.1, 'metric_07') > 50"
    elif layout == "map":
        key = encode_key(NUMERIC_KEY)
        predicate = (
            "JSONExtractInt(concat('{\"f\":',winner.2['" + key + "'],'}'), 'f') > 50"
        )
    elif layout == "dedicated":
        # Typed Int64 slot sits at winner.2.
        predicate = "winner.2 > 50"
    else:
        # String token slot; winner.2 holds the exact numeric token.
        predicate = "JSONExtractInt(concat('{\"f\":',winner.2,'}'), 'f') > 50"
    return f"SELECT count() FROM {view} WHERE {predicate}"


@dataclass(frozen=True)
class ViewMeasurement:
    layout: str
    succeeded: bool
    error: str
    read_bytes: int
    selected_marks: int
    peak_memory: int
    duration_ms: float
    result_check: str


def _soft_run(client: ClickHouseProbe, sql: str, query_id: str) -> tuple[bool, str]:
    """Run a query capturing resource failures instead of raising."""
    try:
        client._next_query_id = query_id
        try:
            client.run(sql)
        finally:
            client._next_query_id = None
        return True, ""
    except ProbeError as error:
        return False, str(error)


def build_view_probe(client: ClickHouseProbe, database: str, rows: int, repeats: int) -> dict:
    validate_database(database)
    tables = provision_layout_tables(client, database, rows)
    for layout in VIEW_LAYOUTS:
        client.run(view_ddl(layout, f"{database}.{tables[layout]}"))

    measurements: list[ViewMeasurement] = []
    for layout in VIEW_LAYOUTS:
        sql = view_trend_query(layout) + " SETTINGS max_threads=2"
        samples = []
        last_error = ""
        for repeat in range(repeats):
            query_id = f"evia-g2-{layout}-{repeat}"
            client.run("SYSTEM DROP MARK CACHE")
            ok, error = _soft_run(client, sql, query_id)
            client.run("SYSTEM FLUSH LOGS")
            record = ""
            for _ in range(20):
                record = client.scalar(
                    "SELECT read_bytes, ProfileEvents['SelectedMarks'] AS selected_marks, "
                    "memory_usage AS peak_memory, query_duration_ms, type, exception_code "
                    "FROM system.query_log WHERE query_id='" + query_id + "' "
                    "ORDER BY event_time_microseconds DESC LIMIT 1 FORMAT JSONEachRow"
                )
                if record:
                    break
                time.sleep(0.5)
            if not record:
                raise ProbeError(f"no query_log row for {query_id}")
            samples.append(json.loads(record))
            if not ok:
                last_error = error
        stable = samples[-1]
        # A failed query still logs an ExceptionBeforeStart/ExceptionWhileProcessing row.
        succeeded = stable.get("type") == "QueryFinish" and not last_error
        result_check = ""
        if succeeded:
            result_check = client.scalar(
                f"SELECT toString(count()) FROM ({view_trend_query(layout)}) FORMAT TSV"
            ).strip()
        measurements.append(
            ViewMeasurement(
                layout=layout,
                succeeded=succeeded,
                error=last_error,
                read_bytes=int(stable["read_bytes"]),
                selected_marks=int(stable["selected_marks"]),
                peak_memory=int(stable["peak_memory"]),
                duration_ms=float(stable["query_duration_ms"]),
                result_check=result_check,
            )
        )

    return {
        "gate": "G2 view-layer argMin tuple",
        "database": database,
        "rows": rows,
        "repeats": repeats,
        "measurements": [m.__dict__ for m in measurements],
    }


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="G2 view-layer argMin tuple physical-read probe")
    sub = parser.add_subparsers(dest="command", required=True)
    run = sub.add_parser("run", help="create isolated probe tables/views and measure tuple materialization")
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
            evidence = build_view_probe(client, args.database, args.rows, args.repeats)
        finally:
            if not args.keep:
                client.run(f"DROP DATABASE IF EXISTS {args.database} SYNC")
        print(json.dumps(evidence, indent=2, sort_keys=True))
        return 0
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
