#!/usr/bin/env python3
"""Scale matrix for retention enforcement disk headroom.

The retention enforcement drill on the dev 512 MiB tmpfs stack proved the
mutation mechanism on 12.2k rows: a synchronous ALTER DELETE rewrites every
matching part, so old and new parts coexist and the measured peak reached
6.36x the baseline table bytes. That number is a mechanism clue, not a
production scheduling model: it was measured on tiny parts with no merge
settling and a single scale.

This probe answers the capacity question behind long-retention sweeps on the
benchmark ClickHouse (4 GiB memory, host-named disk volume):

* at realistic table scales (100k/500k rows) and aged fractions (25/50/75%),
  how much temporary disk one scoped retention mutation needs relative to the
  settled table size, and whether that ratio is scale-stable after
  OPTIMIZE FINAL settles parts;
* how the ratio responds to the expired fraction, since wholly-expired
  partitions are dropped while mixed parts must be rewritten with survivors;
* whether the production invariants (one synchronous mutation per scope,
  expired rows gone, in-window rows byte-exact retained) hold at scale.

The schema is the real production migration set applied inside an isolated
evia_probe_ret_* database, and the mutation predicate mirrors
RetentionExecutor.scopePredicate exactly. Every SQL builder and the matrix
math are pure functions unit-tested without ClickHouse; only the harness in
the lower section talks to the database. Databases are dropped with SYNC so
back-to-back cells cannot accumulate deferred-delete residue.
"""

from __future__ import annotations

import argparse
import datetime
import hashlib
import json
import os
import re
import shlex
import threading
import time
from dataclasses import dataclass, field
from pathlib import Path

from property_layout_probe import ClickHouseProbe, ProbeError, validate_identifier

ROOT = Path(__file__).resolve().parents[2]
DEFAULT_MIGRATIONS = ROOT / "deploy" / "clickhouse"
DEFAULT_CLIENT = "docker exec evia-benchmark-clickhouse-1 clickhouse-client"

PROBE_DB_PREFIX = "evia_probe_ret"
RAW_TABLE = "event_facts_raw"
SCOPE_COUNT = 6
INSERT_BATCH = 25_000
POLL_INTERVAL_S = 0.25
DEFAULT_SCALES = (100_000, 500_000)
DEFAULT_FRACTIONS = (0.25, 0.50, 0.75)
RETENTION_DAYS = 30
AGED_DAYS = 400
RECENT_DAYS = 5

# Distinct valid UUIDs; never reuse pipeline test constants inside a probe.
PROJECTS = (
    "44444444-4444-4444-8444-444444444444",
    "55555555-5555-4555-8555-555555555555",
    "66666666-6666-4666-8666-666666666666",
)
ENVIRONMENTS = ("development", "production")
SOURCE_ID = "77777777-7777-4777-8777-777777777777"

_UUID_RE = re.compile(r"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$")
_DB_RE = re.compile(r"^" + PROBE_DB_PREFIX + r"_[a-z0-9_]+$")
_DATETIME_RE = re.compile(r"^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3}$")
_ENV_RE = re.compile(r"^(development|staging|production)$")


# ---------------------------------------------------------------------------
# Pure validation and matrix planning
# ---------------------------------------------------------------------------

def validate_probe_database(name: str) -> None:
    if not isinstance(name, str) or not _DB_RE.match(name):
        raise ProbeError(f"unsafe probe database name {name!r}")


def validate_uuid(value: str) -> None:
    if not isinstance(value, str) or not _UUID_RE.match(value):
        raise ProbeError(f"invalid UUID {value!r}")


def validate_environment(value: str) -> None:
    if not isinstance(value, str) or not _ENV_RE.match(value):
        raise ProbeError(f"invalid environment {value!r}")


def format_datetime(instant: float) -> str:
    """Format a UTC epoch instant for DateTime64(3) literals used in predicates."""
    moment = datetime.datetime.utcfromtimestamp(instant)
    return moment.strftime("%Y-%m-%d %H:%M:%S.") + f"{moment.microsecond // 1000:03d}"


def cell_database(rows: int, fraction: float) -> str:
    validate_identifier(RAW_TABLE)
    name = f"{PROBE_DB_PREFIX}_s{rows}_f{int(round(fraction * 100)):02d}"
    validate_probe_database(name)
    return name


@dataclass(frozen=True)
class Cell:
    rows: int
    aged_fraction: float

    @property
    def database(self) -> str:
        return cell_database(self.rows, self.aged_fraction)

    @property
    def aged_rows(self) -> int:
        return int(round(self.rows * self.aged_fraction))

    @property
    def recent_rows(self) -> int:
        return self.rows - self.aged_rows


def plan_cells(scales: tuple[int, ...], fractions: tuple[float, ...]) -> list[Cell]:
    cells = []
    for rows in scales:
        if not isinstance(rows, int) or rows < SCOPE_COUNT:
            raise ProbeError(f"scale must be an integer >= {SCOPE_COUNT}: {rows}")
        for fraction in fractions:
            if not isinstance(fraction, (int, float)) or not 0.0 < fraction < 1.0:
                raise ProbeError(f"aged fraction must be within (0, 1): {fraction}")
            cell = Cell(rows, float(fraction))
            if cell.aged_rows < SCOPE_COUNT or cell.recent_rows < SCOPE_COUNT:
                raise ProbeError(
                    f"cell rows={rows} fraction={fraction} leaves a band under {SCOPE_COUNT}")
            cells.append(cell)
    return cells


def allocate_bands(cell: Cell, scopes: int = SCOPE_COUNT) -> list[tuple[int, int]]:
    """Split aged/recent totals across scopes, deterministically reconciling
    integer remainders to the lowest scope so every row is assigned exactly
    once and every scope keeps both bands populated."""
    if scopes != SCOPE_COUNT:
        raise ProbeError(f"scope count is fixed at {SCOPE_COUNT}, got {scopes}")
    bands: list[tuple[int, int]] = []
    aged_remainder = cell.aged_rows % scopes
    recent_remainder = cell.recent_rows % scopes
    aged_base = cell.aged_rows // scopes
    recent_base = cell.recent_rows // scopes
    for index in range(scopes):
        aged = aged_base + (1 if index < aged_remainder else 0)
        recent = recent_base + (1 if index < recent_remainder else 0)
        bands.append((aged, recent))
    total_aged = sum(aged for aged, _ in bands)
    total_recent = sum(recent for _, recent in bands)
    if total_aged != cell.aged_rows or total_recent != cell.recent_rows:
        raise ProbeError("band allocation failed to reconcile with cell totals")
    if any(aged < 1 or recent < 1 for aged, recent in bands):
        raise ProbeError("band allocation left a scope uncovered")
    return bands


def scope_pairs() -> list[tuple[str, str]]:
    pairs = []
    for project in PROJECTS:
        validate_uuid(project)
        for environment in ENVIRONMENTS:
            validate_environment(environment)
            pairs.append((project, environment))
    if len(pairs) != SCOPE_COUNT:
        raise ProbeError(f"scope pairs {len(pairs)} != {SCOPE_COUNT}")
    return pairs


def scope_predicate(project: str, environment: str, cutoff_literal: str) -> str:
    """Mirror RetentionExecutor.scopePredicate; every interpolated value is
    validated against a closed pattern before splicing (no free text input)."""
    validate_uuid(project)
    validate_environment(environment)
    if not _DATETIME_RE.match(cutoff_literal):
        raise ProbeError(f"invalid cutoff literal {cutoff_literal!r}")
    return (
        f"project_id='{project}' AND environment='{environment}' AND "
        f"event_time < toDateTime64('{cutoff_literal}', 3)"
    )


def delete_mutation_sql(database: str, predicate: str) -> str:
    validate_probe_database(database)
    validate_identifier(RAW_TABLE)
    return (
        f"ALTER TABLE `{database}`.`{RAW_TABLE}` DELETE WHERE {predicate} "
        "SETTINGS mutations_sync=1"
    )


def split_sql_migrations(text: str) -> list[str]:
    """Apply migration files the same way the Go integration helper does:
    drop full-line comments, split on semicolons, ignore empty statements."""
    kept = []
    for line in text.splitlines():
        if line.lstrip().startswith("--"):
            continue
        kept.append(line)
    statements = []
    for statement in "\n".join(kept).split(";"):
        compressed = statement.strip()
        if compressed:
            statements.append(compressed)
    return statements


def headroom_ratio(peak_bytes: int, baseline_bytes: int) -> float:
    if baseline_bytes <= 0:
        raise ProbeError("baseline bytes must be positive")
    return round(peak_bytes / baseline_bytes, 4)


def reclaimed_fraction(before_bytes: int, after_bytes: int) -> float:
    if before_bytes <= 0:
        raise ProbeError("before bytes must be positive")
    return round((before_bytes - after_bytes) / before_bytes, 4)


@dataclass
class ScopeMeasurement:
    project: str
    environment: str
    peak_bytes: int
    elapsed_ms: int


@dataclass
class CellMeasurement:
    cell: Cell
    baseline_bytes: int
    baseline_active_parts: int
    after_bytes: int
    after_active_parts: int
    scopes: list[ScopeMeasurement] = field(default_factory=list)
    mutations_total: int = 0
    mutations_done: int = 0
    rows_after: int = 0
    expired_after: int = 0
    recent_after: int = 0
    disk_free_before: int = 0
    disk_free_after: int = 0

    @property
    def max_peak_bytes(self) -> int:
        return max((item.peak_bytes for item in self.scopes), default=0)

    @property
    def reclaimed_bytes(self) -> int:
        return self.baseline_bytes - self.after_bytes

    def as_dict(self) -> dict:
        return {
            "database": self.cell.database,
            "rows": self.cell.rows,
            "aged_fraction": self.cell.aged_fraction,
            "aged_rows": self.cell.aged_rows,
            "recent_rows": self.cell.recent_rows,
            "scopes": SCOPE_COUNT,
            "baseline_bytes": self.baseline_bytes,
            "baseline_active_parts": self.baseline_active_parts,
            "max_peak_bytes": self.max_peak_bytes,
            "max_headroom_ratio": headroom_ratio(self.max_peak_bytes, self.baseline_bytes),
            "peak_amplification_bytes": self.max_peak_bytes - self.baseline_bytes,
            "after_bytes": self.after_bytes,
            "after_active_parts": self.after_active_parts,
            "reclaimed_bytes": self.reclaimed_bytes,
            "reclaimed_fraction": reclaimed_fraction(self.baseline_bytes, self.after_bytes),
            "mutations_total": self.mutations_total,
            "mutations_done": self.mutations_done,
            "rows_after": self.rows_after,
            "expired_after": self.expired_after,
            "recent_after": self.recent_after,
            "disk_free_before": self.disk_free_before,
            "disk_free_after": self.disk_free_after,
            "per_scope": [
                {
                    "project": item.project,
                    "environment": item.environment,
                    "peak_bytes": item.peak_bytes,
                    "headroom_ratio": headroom_ratio(item.peak_bytes, self.baseline_bytes),
                    "elapsed_ms": item.elapsed_ms,
                }
                for item in self.scopes
            ],
        }


def evaluate_cell(measurement: CellMeasurement) -> dict:
    """Fail-closed production invariants; disk free is observed, never gated."""
    failures = []
    if measurement.mutations_total != SCOPE_COUNT or measurement.mutations_done != SCOPE_COUNT:
        failures.append(
            f"mutations {measurement.mutations_total}/{measurement.mutations_done} "
            f"must be {SCOPE_COUNT}/{SCOPE_COUNT}")
    if measurement.expired_after != 0:
        failures.append(f"expired rows remain after sweep: {measurement.expired_after}")
    if measurement.recent_after != measurement.cell.recent_rows:
        failures.append(
            f"recent rows {measurement.recent_after} != {measurement.cell.recent_rows}")
    if measurement.rows_after != measurement.cell.recent_rows:
        failures.append(f"rows after {measurement.rows_after} != recent total")
    if measurement.after_bytes >= measurement.baseline_bytes:
        failures.append("sweep did not shrink table bytes")
    if measurement.max_peak_bytes < measurement.baseline_bytes:
        failures.append("measured peak cannot be below the settled baseline")
    if len(measurement.scopes) != SCOPE_COUNT:
        failures.append(f"scope measurements {len(measurement.scopes)} != {SCOPE_COUNT}")
    return {
        "database": measurement.cell.database,
        "passed": not failures,
        "failures": failures,
        "max_headroom_ratio": headroom_ratio(
            measurement.max_peak_bytes, measurement.baseline_bytes),
        "reclaimed_fraction": reclaimed_fraction(
            measurement.baseline_bytes, measurement.after_bytes),
    }


# ---------------------------------------------------------------------------
# ClickHouse harness
# ---------------------------------------------------------------------------

def build_row(event_id: str, project: str, environment: str, event_ts: str,
              received_ts: str) -> dict:
    """Fixed-shape fact payload matching the production factRow JSON contract
    (Nullable fields emitted as null, derived layout columns use defaults)."""
    return {
        "source_kind": "browser",
        "project_id": project,
        "environment": environment,
        "source_id": SOURCE_ID,
        "event_id": event_id,
        "event_key": "page_opened",
        "schema_version": 1,
        "release_revision": 1,
        "occurred_at": event_ts,
        "event_time": event_ts,
        "occurred_at_precise": event_ts,
        "event_time_precise": event_ts,
        "received_at": received_ts,
        "canonical_hash": hashlib.sha256(event_id.encode("utf-8")).hexdigest(),
        "uid": "synthetic-user",
        "anonymous_id": None,
        "session_id": None,
        "tab_id": None,
        "sequence": None,
        "page_view_id": None,
        "source_page_view_id": None,
        "navigation_id": None,
        "operation_id": None,
        "route_name": None,
        "route_template": None,
        "source_module": None,
        "navigation_type": None,
        "previous_page_view_id": None,
        "sdk_name": "test",
        "sdk_version": "1.0.0",
        "context_json": "{}",
        "properties_json": '{"count":0,"enabled":false,"title":"Home"}',
        "property_layout_version": 1,
        "property_tokens": {},
    }


class PartsBytePoller:
    """Background sampler of total on-disk parts bytes (active and inactive)
    capturing the old/new part coexistence window of a synchronous mutation."""

    def __init__(self, client: ClickHouseProbe, database: str):
        validate_probe_database(database)
        self.client = client
        self.database = database
        self._stop = threading.Event()
        self._thread: threading.Thread | None = None
        self.peak = 0

    def _sample(self) -> None:
        sql = (
            "SELECT sum(bytes_on_disk) FROM system.parts WHERE database='"
            + self.database + f"' AND table='{RAW_TABLE}' FORMAT TSV"
        )
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


class HeadroomHarness:
    def __init__(self, client: ClickHouseProbe, migrations_dir: Path):
        self.client = client
        self.migrations_dir = migrations_dir

    def _database_client(self, database: str) -> ClickHouseProbe:
        validate_probe_database(database)
        prefix = shlex.join(self.client.argv_prefix) + f" --database {database}"
        return ClickHouseProbe(prefix, self.client.username, self.client.password)

    def provision(self, database: str) -> None:
        validate_probe_database(database)
        admin = self.client
        admin.run(f"DROP DATABASE IF EXISTS {database} SYNC")
        admin.run(f"CREATE DATABASE {database}")
        db_client = self._database_client(database)
        for path in sorted(self.migrations_dir.glob("*.sql")):
            for statement in split_sql_migrations(path.read_text(encoding="utf-8")):
                db_client.run(statement)

    def drop(self, database: str) -> None:
        validate_probe_database(database)
        self.client.run(f"DROP DATABASE {database} SYNC")

    def stream_load(self, database: str, cell: Cell, aged_ts: str, recent_ts: str,
                    received_ts: str) -> None:
        db_client = self._database_client(database)
        remaining = list(allocate_bands(cell))
        pairs = scope_pairs()
        ordinal = 0
        batch: list[str] = []

        def flush() -> None:
            nonlocal batch
            if not batch:
                return
            db_client.run(
                f"INSERT INTO {RAW_TABLE} FORMAT JSONEachRow",
                stdin=("\n".join(batch) + "\n").encode(),
            )
            batch = []

        # Round-robin scopes (and both bands per scope) so every merged part
        # holds all tenants and age bands: worst case for a scoped rewrite.
        while any(aged > 0 or recent > 0 for aged, recent in remaining):
            for scope_index, (project, environment) in enumerate(pairs):
                aged_left, recent_left = remaining[scope_index]
                if aged_left > 0:
                    batch.append(json.dumps(build_row(
                        f"ret-{cell.rows}-{ordinal:08d}",
                        project, environment, aged_ts, received_ts)))
                    ordinal += 1
                    aged_left -= 1
                if recent_left > 0:
                    batch.append(json.dumps(build_row(
                        f"ret-{cell.rows}-{ordinal:08d}",
                        project, environment, recent_ts, received_ts)))
                    ordinal += 1
                    recent_left -= 1
                remaining[scope_index] = (aged_left, recent_left)
                if len(batch) >= INSERT_BATCH:
                    flush()
        flush()
        if ordinal != cell.rows:
            raise ProbeError(f"loaded {ordinal} rows, want {cell.rows}")
        db_client.run(f"OPTIMIZE TABLE {RAW_TABLE} FINAL")

    def table_bytes(self, database: str, active_only: bool) -> int:
        clause = "AND active" if active_only else ""
        value = self.client.scalar(
            "SELECT sum(bytes_on_disk) FROM system.parts WHERE database='"
            + database + f"' AND table='{RAW_TABLE}' {clause} FORMAT TSV")
        return int(value)

    def active_parts(self, database: str) -> int:
        return int(self.client.scalar(
            "SELECT count() FROM system.parts WHERE database='"
            + database + f"' AND table='{RAW_TABLE}' AND active FORMAT TSV"))

    def disk_free(self) -> int:
        return int(self.client.scalar(
            "SELECT free_space FROM system.disks WHERE name='default' FORMAT TSV"))

    def row_count(self, database: str, where: str = "1=1") -> int:
        return int(self.client.scalar(
            f"SELECT count() FROM {database}.{RAW_TABLE} WHERE {where} FORMAT TSV"))

    def mutation_status(self, database: str) -> tuple[int, int]:
        row = self.client.scalar(
            "SELECT count(), countIf(is_done=1) FROM system.mutations WHERE database='"
            + database + f"' AND table='{RAW_TABLE}' FORMAT TSV")
        total, done = row.split()
        return int(total), int(done)

    def sweep(self, database: str, cutoff_literal: str) -> list[ScopeMeasurement]:
        db_client = self._database_client(database)
        measurements = []
        for project, environment in scope_pairs():
            predicate = scope_predicate(project, environment, cutoff_literal)
            poller = PartsBytePoller(self.client, database)
            poller.start()
            started = time.monotonic()
            db_client.run(delete_mutation_sql(database, predicate))
            elapsed_ms = int((time.monotonic() - started) * 1000)
            peak = poller.stop()
            measurements.append(ScopeMeasurement(project, environment, peak, elapsed_ms))
        return measurements


def run_probe(client: ClickHouseProbe, migrations_dir: Path, cells: list[Cell],
              keep: bool) -> dict:
    harness = HeadroomHarness(client, migrations_dir)
    now = time.time()
    aged_ts = format_datetime(now - AGED_DAYS * 86400)
    recent_ts = format_datetime(now - RECENT_DAYS * 86400)
    cutoff_ts = format_datetime(now - RETENTION_DAYS * 86400)
    moment = datetime.datetime.utcfromtimestamp(now)
    received_ts = moment.strftime("%Y-%m-%d %H:%M:%S.") + f"{moment.microsecond:06d}000"

    results = []
    verdicts = []
    for cell in cells:
        try:
            harness.provision(cell.database)
            harness.stream_load(cell.database, cell, aged_ts, recent_ts, received_ts)
            baseline = harness.table_bytes(cell.database, active_only=True)
            baseline_parts = harness.active_parts(cell.database)
            free_before = harness.disk_free()
            scopes = harness.sweep(cell.database, cutoff_ts)
            client.run("SYSTEM FLUSH LOGS")
            total, done = harness.mutation_status(cell.database)
            after = harness.table_bytes(cell.database, active_only=True)
            after_parts = harness.active_parts(cell.database)
            free_after = harness.disk_free()
            expired_after = 0
            for project, environment in scope_pairs():
                expired_after += harness.row_count(
                    cell.database, scope_predicate(project, environment, cutoff_ts))
            recent_after = harness.row_count(
                cell.database, f"event_time >= toDateTime64('{cutoff_ts}', 3)")
            measurement = CellMeasurement(
                cell=cell, baseline_bytes=baseline, baseline_active_parts=baseline_parts,
                after_bytes=after, after_active_parts=after_parts, scopes=scopes,
                mutations_total=total, mutations_done=done,
                rows_after=harness.row_count(cell.database), expired_after=expired_after,
                recent_after=recent_after, disk_free_before=free_before,
                disk_free_after=free_after)
            results.append(measurement.as_dict())
            verdicts.append(evaluate_cell(measurement))
        finally:
            # Drop per cell so only one scale's data is resident at a time and
            # the benchmark volume never accumulates deferred-delete residue.
            if not keep:
                harness.drop(cell.database)

    passed = all(item["passed"] for item in verdicts)
    return {
        "status": "passed" if passed else "failed",
        "probe": "retention-headroom",
        "cutoff": cutoff_ts,
        "cells": results,
        "verdicts": verdicts,
    }


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    run = parser.add_argument_group("run")
    run.add_argument("--client", default=os.environ.get("EVIA_CH_CLIENT", DEFAULT_CLIENT))
    run.add_argument("--username", default=os.environ.get("EVIA_CH_USERNAME", "evia"))
    run.add_argument("--password", default=os.environ.get("EVIA_CH_PASSWORD", ""))
    run.add_argument("--migrations-dir", default=str(DEFAULT_MIGRATIONS))
    run.add_argument("--scales", default=",".join(str(value) for value in DEFAULT_SCALES))
    run.add_argument("--fractions", default=",".join(str(value) for value in DEFAULT_FRACTIONS))
    run.add_argument("--keep", action="store_true")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    scales = tuple(int(value) for value in args.scales.split(",") if value)
    fractions = tuple(float(value) for value in args.fractions.split(",") if value)
    cells = plan_cells(scales, fractions)
    client = ClickHouseProbe(args.client, args.username, args.password)
    report = run_probe(client, Path(args.migrations_dir), cells, args.keep)
    print(json.dumps(report, indent=2, sort_keys=True))
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
