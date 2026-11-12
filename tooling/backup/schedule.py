"""Scheduled backup/recovery drill orchestrator for Evia.

The single-shot drill tools (``drill.py`` for PostgreSQL and
``clickhouse_drill.py`` for ClickHouse) verify one restore cycle when invoked
manually. Production deployments need the cycle to run unattended on a timer,
retain a bounded number of artifacts and expose machine-readable health so a
missed or failed drill pages the on-call rotation instead of going unnoticed.

This module adds that layer with the standard library only:

* ``run-once`` executes every configured target sequentially, reusing the
  existing drill functions, and never lets one target failure abort the others.
* A flock-based lock prevents overlapping timer runs.
* Cumulative state survives restarts and is rendered as a node_exporter
  textfile metrics document, atomically replaced on every run.
* Artifact pruning only touches files matching the managed naming patterns,
  so unrelated files in the backup directory can never be deleted.
* Credentials stay in environment variables referenced by name from the
  config; they are never written into reports or metric labels.
"""
from __future__ import annotations

import argparse
import dataclasses
import datetime as dt
import fcntl
import json
import os
import re
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import clickhouse_drill  # noqa: E402
import drill  # noqa: E402

SCHEDULE_FORMAT_VERSION = 1
STATE_FORMAT_VERSION = 1
MAX_TARGETS = 8
DEFAULT_RETENTION_KEEP = 7
MIN_KEEP = 1
MAX_KEEP = 100
TARGET_NAME_RE = re.compile(r"^[a-z0-9][a-z0-9_-]{0,31}$")
ENV_NAME_RE = re.compile(r"^[A-Z_][A-Z0-9_]{0,64}$")
KINDS = ("postgres", "clickhouse")

# Metric names are also registered in deploy/monitoring/textfile-metrics.json;
# check_monitoring.py cross-checks the two sources so a typo cannot reach an
# alert rule silently.
METRIC_SPECS = {
    "evia_backup_up": ("gauge", "1 if the last drill for this target verified a restore, otherwise 0"),
    "evia_backup_last_attempt_timestamp_seconds": ("gauge", "Unix timestamp of the last scheduled drill attempt"),
    "evia_backup_last_success_timestamp_seconds": ("gauge", "Unix timestamp of the last successful drill, 0 if never"),
    "evia_backup_success_total": ("counter", "Cumulative successful drill runs since the state file was created"),
    "evia_backup_failure_total": ("counter", "Cumulative failed drill runs since the state file was created"),
    "evia_backup_duration_seconds": ("gauge", "Wall-clock duration of the last drill attempt"),
}

PG_DUMP_RE = re.compile(r"^evia-pg-backup-\d{8}T\d{6}Z\.dump$")
PG_MANIFEST_RE = re.compile(r"^evia-pg-backup-\d{8}T\d{6}Z\.manifest\.json$")
PG_REPORT_RE = re.compile(r"^evia-restore-drill-\d{8}T\d{6}Z\.report\.json$")
CH_BACKUP_RE = re.compile(r"^evia_ch_backup_\d{8}t\d{6}_[0-9a-f]{6}$")
CH_MANIFEST_RE = re.compile(r"^evia_ch_backup_\d{8}t\d{6}_[0-9a-f]{6}\.manifest\.json$")


class ScheduleError(ValueError):
    """Raised for invalid configuration or unsafe runtime state."""


def utc_now() -> dt.datetime:
    return dt.datetime.now(dt.timezone.utc)


def utc_stamp(value: dt.datetime) -> str:
    return value.astimezone(dt.timezone.utc).isoformat()


@dataclasses.dataclass(frozen=True)
class Target:
    name: str
    kind: str
    output_dir: Path
    retention_keep: int
    # PostgreSQL target.
    dsn_env: str | None = None
    schema: str = drill.DEFAULT_SCHEMA
    # ClickHouse target.
    database: str | None = None
    ch_client_env: str = "EVIA_CH_CLIENT"
    ch_username_env: str = "EVIA_CH_USERNAME"
    ch_password_env: str = "EVIA_CH_PASSWORD"
    timeout_seconds: int = clickhouse_drill.DEFAULT_TIMEOUT_SECONDS

    def labels(self) -> dict[str, str]:
        return {"target": self.name, "kind": self.kind}


def parse_target(raw: dict) -> Target:
    if not isinstance(raw, dict):
        raise ScheduleError("each target must be an object")
    name = raw.get("name")
    if not isinstance(name, str) or not TARGET_NAME_RE.fullmatch(name):
        raise ScheduleError(f"target name {name!r} must match {TARGET_NAME_RE.pattern}")
    kind = raw.get("kind")
    if kind not in KINDS:
        raise ScheduleError(f"target {name}: kind must be one of {KINDS}")
    output = raw.get("output_dir")
    if not isinstance(output, str) or not output:
        raise ScheduleError(f"target {name}: output_dir is required")
    keep = raw.get("retention_keep", DEFAULT_RETENTION_KEEP)
    if not isinstance(keep, int) or not MIN_KEEP <= keep <= MAX_KEEP:
        raise ScheduleError(f"target {name}: retention_keep must be between {MIN_KEEP} and {MAX_KEEP}")
    target = Target(name=name, kind=kind, output_dir=Path(output), retention_keep=keep)
    if kind == "postgres":
        dsn_env = raw.get("dsn_env")
        if not isinstance(dsn_env, str) or not ENV_NAME_RE.fullmatch(dsn_env):
            raise ScheduleError(f"target {name}: dsn_env must name an uppercase environment variable")
        schema = raw.get("schema", drill.DEFAULT_SCHEMA)
        if not isinstance(schema, str) or not re.fullmatch(r"[a-z_][a-z0-9_]*", schema):
            raise ScheduleError(f"target {name}: invalid schema {schema!r}")
        object.__setattr__(target, "dsn_env", dsn_env)
        object.__setattr__(target, "schema", schema)
    else:
        database = raw.get("database")
        if not isinstance(database, str):
            raise ScheduleError(f"target {name}: database is required for ClickHouse targets")
        try:
            clickhouse_drill.validate_identifier(database)
        except ValueError as error:
            raise ScheduleError(f"target {name}: {error}") from error
        object.__setattr__(target, "database", database)
        for field, env_field in (("ch_client_env", "ch_client_env"),
                                 ("ch_username_env", "ch_username_env"),
                                 ("ch_password_env", "ch_password_env")):
            value = raw.get(field, getattr(target, field))
            if not isinstance(value, str) or not ENV_NAME_RE.fullmatch(value):
                raise ScheduleError(f"target {name}: {field} must name an environment variable")
            object.__setattr__(target, field, value)
        timeout = raw.get("timeout_seconds", target.timeout_seconds)
        if not isinstance(timeout, int) or not 60 <= timeout <= 1800:
            raise ScheduleError(f"target {name}: timeout_seconds must be between 60 and 1800")
        object.__setattr__(target, "timeout_seconds", timeout)
    return target


def load_config(path: Path) -> tuple[list[Target], Path, Path]:
    try:
        document = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as error:
        raise ScheduleError(f"config not found: {path}") from error
    except json.JSONDecodeError as error:
        raise ScheduleError(f"config is not valid JSON: {error}") from error
    if document.get("version") != SCHEDULE_FORMAT_VERSION:
        raise ScheduleError(f"unsupported config version: {document.get('version')!r}")
    raw_targets = document.get("targets")
    if not isinstance(raw_targets, list) or not raw_targets:
        raise ScheduleError("targets must be a non-empty list")
    if len(raw_targets) > MAX_TARGETS:
        raise ScheduleError(f"at most {MAX_TARGETS} targets are supported in one schedule")
    targets = [parse_target(item) for item in raw_targets]
    names = [target.name for target in targets]
    if len(set(names)) != len(names):
        raise ScheduleError("target names must be unique")
    raw_state_dir = document.get("state_dir")
    raw_metrics_path = document.get("metrics_path")
    if not isinstance(raw_state_dir, str) or not raw_state_dir.strip():
        raise ScheduleError("state_dir must be a non-empty string")
    if not isinstance(raw_metrics_path, str) or not raw_metrics_path.strip():
        raise ScheduleError("metrics_path must be a non-empty string")
    return targets, Path(raw_state_dir), Path(raw_metrics_path)


def redact_secret(text: str, secret: str) -> str:
    if not secret:
        return text
    return text.replace(secret, "***")


def run_postgres_drill(target: Target) -> dict:
    dsn = os.environ.get(target.dsn_env or "", "")
    if not dsn:
        raise ScheduleError(f"environment variable {target.dsn_env} is not set")
    target.output_dir.mkdir(parents=True, exist_ok=True)
    # PG artifacts (dump + manifest) are always retained by do_drill; the keep
    # flag only controls the scratch database, which the scheduler always drops.
    return drill.do_drill(dsn, target.output_dir, target.schema, keep=False)


def run_clickhouse_drill(target: Target) -> dict:
    client = clickhouse_drill.parse_client(
        os.environ.get(target.ch_client_env),
        os.environ.get(target.ch_username_env),
        os.environ.get(target.ch_password_env),
        target.timeout_seconds,
    )
    target.output_dir.mkdir(parents=True, exist_ok=True)
    return clickhouse_drill.do_drill(client, target.database or "", target.output_dir, keep=True)


DEFAULT_RUNNERS = {
    "postgres": run_postgres_drill,
    "clickhouse": run_clickhouse_drill,
}


def prune_files(directory: Path, pattern: re.Pattern[str], keep: int) -> list[str]:
    """Delete the oldest managed entries beyond ``keep``; unmanaged files stay."""
    entries = [path for path in directory.iterdir() if pattern.fullmatch(path.name)]
    entries.sort(key=lambda path: path.name, reverse=True)
    removed: list[str] = []
    for path in entries[keep:]:
        if path.is_dir():
            for child in sorted(path.rglob("*"), reverse=True):
                child.unlink() if child.is_file() else child.rmdir()
            path.rmdir()
        else:
            path.unlink()
        removed.append(path.name)
    return removed


def prune_target(target: Target) -> list[str]:
    directory = target.output_dir
    if not directory.exists():
        return []
    if target.kind == "postgres":
        removed = prune_files(directory, PG_DUMP_RE, target.retention_keep)
        removed += prune_files(directory, PG_MANIFEST_RE, target.retention_keep)
        removed += prune_files(directory, PG_REPORT_RE, target.retention_keep)
    else:
        removed = prune_files(directory, CH_BACKUP_RE, target.retention_keep)
        removed += prune_files(directory, CH_MANIFEST_RE, target.retention_keep)
    return removed


def load_state(path: Path) -> dict:
    if not path.exists():
        return {"version": STATE_FORMAT_VERSION, "targets": {}}
    document = json.loads(path.read_text(encoding="utf-8"))
    if document.get("version") != STATE_FORMAT_VERSION or not isinstance(document.get("targets"), dict):
        raise ScheduleError(f"state file {path} is malformed; refusing to overwrite counters")
    return document


def write_state_atomic(path: Path, state: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + ".tmp")
    tmp.write_text(json.dumps(state, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    os.replace(tmp, path)


def render_textfile(state: dict) -> str:
    lines: list[str] = []
    for metric, (kind, help_text) in METRIC_SPECS.items():
        lines.append(f"# HELP {metric} {help_text}")
        lines.append(f"# TYPE {metric} {kind}")
    targets = state["targets"]
    for name in sorted(targets):
        entry = targets[name]
        labels = f'target="{name}",kind="{entry["kind"]}"'
        lines.append(f'evia_backup_up{{{labels}}} {entry["up"]}')
        lines.append(f'evia_backup_last_attempt_timestamp_seconds{{{labels}}} {entry["last_attempt_ts"]}')
        lines.append(f'evia_backup_last_success_timestamp_seconds{{{labels}}} {entry["last_success_ts"]}')
        lines.append(f'evia_backup_success_total{{{labels}}} {entry["success_total"]}')
        lines.append(f'evia_backup_failure_total{{{labels}}} {entry["failure_total"]}')
        lines.append(f'evia_backup_duration_seconds{{{labels}}} {entry["last_duration_seconds"]:.6f}')
    return "\n".join(lines) + "\n"


def write_metrics_atomic(path: Path, state: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + ".tmp")
    tmp.write_text(render_textfile(state), encoding="utf-8")
    os.replace(tmp, path)


def acquire_lock(path: Path) -> object:
    path.parent.mkdir(parents=True, exist_ok=True)
    handle = path.open("w")
    try:
        fcntl.flock(handle.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError as error:
        handle.close()
        raise ScheduleError("another backup schedule run is already in progress") from error
    handle.write(str(os.getpid()))
    handle.flush()
    return handle


def run_once(config_path: Path, runners: dict | None = None) -> int:
    targets, state_dir, metrics_path = load_config(config_path)
    runners = runners or DEFAULT_RUNNERS
    lock_handle = acquire_lock(state_dir / "schedule.lock")
    try:
        state_path = state_dir / "schedule-state.json"
        state = load_state(state_path)
        run_started = utc_now()
        overall_ok = True
        run_report = {
            "run_at": utc_stamp(run_started),
            "config": str(config_path),
            "targets": [],
        }
        for target in targets:
            started = time.monotonic()
            attempted_at = utc_now()
            if target.kind == "postgres":
                try:
                    secret = drill.parse_dsn(os.environ.get(target.dsn_env or "", "")).password
                except ValueError:
                    secret = ""
            else:
                secret = os.environ.get(target.ch_password_env, "")
            entry = state["targets"].setdefault(
                target.name,
                {
                    "kind": target.kind,
                    "up": 0,
                    "last_attempt_ts": 0,
                    "last_success_ts": 0,
                    "success_total": 0,
                    "failure_total": 0,
                    "last_duration_seconds": 0.0,
                },
            )
            if entry["kind"] != target.kind:
                raise ScheduleError(f"target {target.name} changed kind; reset state explicitly")
            entry["last_attempt_ts"] = int(attempted_at.timestamp())
            target_report: dict = {"target": target.name, "kind": target.kind, "ok": False}
            try:
                detail = runners[target.kind](target)
                entry["up"] = 1
                entry["last_success_ts"] = int(attempted_at.timestamp())
                entry["success_total"] += 1
                target_report["ok"] = True
                target_report["detail"] = detail
                target_report["pruned"] = prune_target(target)
            except Exception as error:  # noqa: BLE001 - report every failure, fail closed later
                entry["up"] = 0
                entry["failure_total"] += 1
                target_report["error"] = redact_secret(str(error), secret)
                overall_ok = False
            finally:
                entry["last_duration_seconds"] = round(time.monotonic() - started, 6)
                state["last_run_at"] = utc_stamp(run_started)
                write_state_atomic(state_path, state)
                write_metrics_atomic(metrics_path, state)
                run_report["targets"].append(target_report)
        report_path = state_dir / f"schedule-run-{run_started.strftime('%Y%m%dT%H%M%SZ')}.json"
        write_state_atomic(report_path, run_report)
        print(json.dumps(run_report, indent=2, sort_keys=True, ensure_ascii=False))
        return 0 if overall_ok else 1
    finally:
        fcntl.flock(lock_handle.fileno(), fcntl.LOCK_UN)
        lock_handle.close()


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Evia scheduled backup drill orchestrator")
    sub = parser.add_subparsers(dest="command", required=True)
    run = sub.add_parser("run-once", help="run every configured drill target once")
    run.add_argument("--config", required=True, type=Path)
    validate = sub.add_parser("validate", help="validate config and credential environment without drilling")
    validate.add_argument("--config", required=True, type=Path)
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        targets, _, _ = load_config(args.config)
        if args.command == "validate":
            for target in targets:
                if target.kind == "postgres" and not os.environ.get(target.dsn_env or ""):
                    raise ScheduleError(f"environment variable {target.dsn_env} is not set")
            print(json.dumps({"ok": True, "targets": [t.name for t in targets]}))
            return 0
        return run_once(args.config)
    except ScheduleError as error:
        print(f"backup schedule failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
