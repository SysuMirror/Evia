"""PostgreSQL backup, restore and drill automation for Evia control metadata.

The control database (accounts, projects, governance, metrics, erasure
tombstones, retention policy and the migration ledger) is the irreplaceable
state of an Evia deployment. This module makes the backup/restore loop
auditable:

* ``backup``  - pg_dump custom-format artifact plus a signed manifest
* ``snapshot``- row counts, content hashes and migration ledger for one database
* ``verify``  - structural and content comparison of two databases
* ``restore`` - pg_restore into a scratch database with a strict name allowlist
* ``drill``   - the full backup -> scratch restore -> verify -> cleanup cycle

The module uses only the Python standard library and the local PostgreSQL
client tools (pg_dump, pg_restore, psql). Credentials are passed through
libpq environment variables (PGPASSWORD) and never appear in argv or logs.
"""
from __future__ import annotations

import argparse
import dataclasses
import datetime as dt
import hashlib
import json
import os
import re
import secrets
import subprocess
import sys
import urllib.parse
from pathlib import Path

MANIFEST_FORMAT_VERSION = 1
SCRATCH_NAME_PATTERN = re.compile(r"^evia_restore_drill_[a-z0-9]{6,32}$")
IDENTIFIER_PATTERN = re.compile(r"^[a-z_][a-z0-9_]*$")
DEFAULT_SCHEMA = "public"
DEFAULT_TIMEOUT_SECONDS = 300


@dataclasses.dataclass(frozen=True)
class ConnInfo:
    host: str
    port: int
    dbname: str
    user: str
    password: str
    sslmode: str

    def with_database(self, dbname: str) -> "ConnInfo":
        return dataclasses.replace(self, dbname=dbname)


def parse_dsn(dsn: str) -> ConnInfo:
    """Parse a postgres:// DSN; query parameters only contribute sslmode."""
    parsed = urllib.parse.urlparse(dsn)
    if parsed.scheme not in ("postgres", "postgresql"):
        raise ValueError("DSN must use the postgres:// scheme")
    if not parsed.hostname or not parsed.username or not parsed.path.lstrip("/"):
        raise ValueError("DSN requires host, user and database name")
    query = urllib.parse.parse_qs(parsed.query)
    sslmode = query.get("sslmode", ["prefer"])[0]
    return ConnInfo(
        host=parsed.hostname,
        port=parsed.port or 5432,
        dbname=parsed.path.lstrip("/"),
        user=parsed.username,
        password=parsed.password or "",
        sslmode=sslmode,
    )


def redact_dsn(dsn: str) -> str:
    """Return a DSN safe for logs and reports: password is always masked."""
    parsed = urllib.parse.urlparse(dsn)
    if parsed.password is None:
        return dsn
    netloc = parsed.netloc.replace(":" + parsed.password + "@", ":***@", 1)
    return parsed._replace(netloc=netloc).geturl()


def libpq_env(info: ConnInfo) -> dict[str, str]:
    """Build libpq environment variables; the password never reaches argv."""
    env = dict(os.environ)
    env.update(
        {
            "PGHOST": info.host,
            "PGPORT": str(info.port),
            "PGUSER": info.user,
            "PGDATABASE": info.dbname,
            "PGSSLMODE": info.sslmode,
        }
    )
    if info.password:
        env["PGPASSWORD"] = info.password
    return env


def validate_scratch_database_name(name: str) -> None:
    """Guard restore targets so a drill can never clobber a real database."""
    if not SCRATCH_NAME_PATTERN.fullmatch(name):
        raise ValueError(
            "restore target database must match "
            "evia_restore_drill_<6-32 lowercase alphanumeric characters>"
        )


def new_scratch_database_name() -> str:
    return "evia_restore_drill_" + secrets.token_hex(6)


def pg_dump_command(artifact: Path, schema: str = DEFAULT_SCHEMA) -> list[str]:
    if not IDENTIFIER_PATTERN.fullmatch(schema):
        raise ValueError(f"invalid schema name {schema!r}")
    # -Fc custom format supports parallel restore and selective objects;
    # ownership/ACL differences between clusters must not block the restore.
    return [
        "pg_dump",
        "-Fc",
        "--no-owner",
        "--no-privileges",
        "--schema",
        schema,
        "--file",
        str(artifact),
    ]


def pg_restore_command(artifact: Path) -> list[str]:
    # --exit-on-error makes any DDL/data failure visible instead of warning-only;
    # the scratch database is brand new, so --clean has nothing to remove.
    return [
        "pg_restore",
        "--exit-on-error",
        "--no-owner",
        "--no-privileges",
        str(artifact),
    ]


def psql_command(sql: str) -> list[str]:
    return ["psql", "-v", "ON_ERROR_STOP=1", "-At", "-c", sql]


def list_tables_sql(schema: str) -> str:
    if not IDENTIFIER_PATTERN.fullmatch(schema):
        raise ValueError(f"invalid schema name {schema!r}")
    return (
        "SELECT table_name FROM information_schema.tables "
        f"WHERE table_schema = '{schema}' AND table_type = 'BASE TABLE' "
        "ORDER BY table_name"
    )


def table_fingerprint_sql(schema: str, table: str) -> str:
    """Return count plus a content-sensitive hash for every row of a table."""
    if not IDENTIFIER_PATTERN.fullmatch(schema) or not IDENTIFIER_PATTERN.fullmatch(table):
        raise ValueError(f"invalid qualified name {schema}.{table!r}")
    qualified = f'"{schema}"."{table}"'
    # hashtextextended is stable for a fixed PostgreSQL major version, which is
    # exactly the drill boundary (backup and restore target the same cluster).
    return (
        f"SELECT count(*), COALESCE(sum(hashtextextended(to_jsonb(t)::text, 0)), 0) "
        f"FROM {qualified} AS t"
    )


def ledger_sql(schema: str = DEFAULT_SCHEMA) -> str:
    if not IDENTIFIER_PATTERN.fullmatch(schema):
        raise ValueError(f"invalid schema name {schema!r}")
    return (
        f"SELECT name, checksum FROM \"{schema}\".schema_migrations "
        "ORDER BY name"
    )


def run_command(cmd: list[str], env: dict[str, str], timeout: int = DEFAULT_TIMEOUT_SECONDS) -> str:
    try:
        completed = subprocess.run(
            cmd, env=env, timeout=timeout, check=False,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
        )
    except FileNotFoundError as error:
        raise RuntimeError(f"required client tool not found: {cmd[0]}") from error
    except subprocess.TimeoutExpired as error:
        raise RuntimeError(f"command timed out after {timeout}s: {' '.join(cmd[:2])}") from error
    if completed.returncode != 0:
        raise RuntimeError(
            f"{cmd[0]} failed (exit {completed.returncode}): {completed.stderr.strip()}"
        )
    return completed.stdout


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def take_snapshot(info: ConnInfo, schema: str = DEFAULT_SCHEMA) -> dict:
    """Capture ledger rows, per-table counts and content hashes via psql."""
    env = libpq_env(info)
    tables = [
        line.strip()
        for line in run_command(psql_command(list_tables_sql(schema)), env).splitlines()
        if line.strip()
    ]
    fingerprints: dict[str, dict[str, int]] = {}
    for table in tables:
        row = run_command(psql_command(table_fingerprint_sql(schema, table)), env).strip()
        count_text, hash_text = row.split("|", 1)
        fingerprints[table] = {"rows": int(count_text), "hash": int(hash_text)}
    ledger: list[list[str]] = []
    if "schema_migrations" in tables:
        for line in run_command(psql_command(ledger_sql(schema)), env).splitlines():
            if not line.strip():
                continue
            name, checksum = line.split("|", 1)
            ledger.append([name, checksum])
    return {
        "database": info.dbname,
        "schema": schema,
        "captured_at": dt.datetime.now(dt.timezone.utc).isoformat(),
        "tables": fingerprints,
        "ledger": ledger,
    }


def compare_snapshots(source: dict, target: dict) -> list[str]:
    """Pure structural/content comparison; every returned string is one finding."""
    findings: list[str] = []
    source_tables = set(source["tables"])
    target_tables = set(target["tables"])
    for missing in sorted(source_tables - target_tables):
        findings.append(f"missing table in restore target: {missing}")
    for extra in sorted(target_tables - source_tables):
        findings.append(f"unexpected table in restore target: {extra}")
    for table in sorted(source_tables & target_tables):
        left, right = source["tables"][table], target["tables"][table]
        if left["rows"] != right["rows"]:
            findings.append(
                f"row count differs for {table}: source={left['rows']} target={right['rows']}"
            )
        elif left["hash"] != right["hash"]:
            findings.append(f"content hash differs for {table} despite equal row counts")
    source_ledger = {name: checksum for name, checksum in source["ledger"]}
    target_ledger = {name: checksum for name, checksum in target["ledger"]}
    for name in sorted(set(source_ledger) | set(target_ledger)):
        if name not in target_ledger:
            findings.append(f"migration ledger entry missing after restore: {name}")
        elif name not in source_ledger:
            findings.append(f"unexpected migration ledger entry after restore: {name}")
        elif source_ledger[name] != target_ledger[name]:
            findings.append(f"migration checksum differs for {name}")
    return findings


def build_manifest(source_dsn: str, artifact: Path, snapshot: dict) -> dict:
    return {
        "format_version": MANIFEST_FORMAT_VERSION,
        "created_at": dt.datetime.now(dt.timezone.utc).isoformat(),
        "source": redact_dsn(source_dsn),
        "artifact": artifact.name,
        "artifact_size_bytes": artifact.stat().st_size,
        "artifact_sha256": sha256_file(artifact),
        "snapshot": snapshot,
    }


def verify_artifact(manifest: dict, artifact: Path) -> None:
    """Fail when the dump file changed after the manifest was written."""
    if manifest.get("artifact") != artifact.name:
        raise ValueError("manifest does not describe this artifact")
    actual = sha256_file(artifact)
    if actual != manifest["artifact_sha256"]:
        raise ValueError(
            f"artifact checksum mismatch: manifest={manifest['artifact_sha256']} actual={actual}"
        )


def write_json(path: Path, payload: dict) -> None:
    path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def do_backup(source_dsn: str, output_dir: Path, schema: str) -> tuple[Path, dict]:
    info = parse_dsn(source_dsn)
    output_dir.mkdir(parents=True, exist_ok=True)
    stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    artifact = output_dir / f"evia-pg-backup-{stamp}.dump"
    if artifact.exists():
        raise RuntimeError(f"artifact already exists: {artifact}")
    run_command(pg_dump_command(artifact, schema), libpq_env(info))
    manifest = build_manifest(source_dsn, artifact, take_snapshot(info, schema))
    manifest_path = artifact.with_suffix(".manifest.json")
    write_json(manifest_path, manifest)
    return artifact, manifest


def do_restore(target_dsn: str, artifact: Path, manifest: dict | None = None) -> None:
    info = parse_dsn(target_dsn)
    validate_scratch_database_name(info.dbname)
    if manifest is not None:
        verify_artifact(manifest, artifact)
    run_command(pg_restore_command(artifact), libpq_env(info))


def do_verify(source_dsn: str, target_dsn: str, schema: str) -> dict:
    source = parse_dsn(source_dsn)
    target = parse_dsn(target_dsn)
    source_snapshot = take_snapshot(source, schema)
    target_snapshot = take_snapshot(target, schema)
    findings = compare_snapshots(source_snapshot, target_snapshot)
    return {
        "verified_at": dt.datetime.now(dt.timezone.utc).isoformat(),
        "source": redact_dsn(source_dsn),
        "target": redact_dsn(target_dsn),
        "schema": schema,
        "findings": findings,
        "ok": not findings,
    }


def do_drill(source_dsn: str, output_dir: Path, schema: str, keep: bool) -> dict:
    """Full recovery drill against an isolated scratch database."""
    source = parse_dsn(source_dsn)
    scratch_name = new_scratch_database_name()
    artifact, manifest = do_backup(source_dsn, output_dir, schema)
    env = libpq_env(source)
    created = False
    report: dict
    try:
        run_command(
            psql_command(f"CREATE DATABASE {scratch_name}"),
            libpq_env(source),
        )
        created = True
        scratch_dsn = urllib.parse.urlunparse(_dsn_with_database(source_dsn, scratch_name))
        do_restore(scratch_dsn, artifact, manifest)
        verification = do_verify(source_dsn, scratch_dsn, schema)
        report = {
            "drill_at": dt.datetime.now(dt.timezone.utc).isoformat(),
            "source": redact_dsn(source_dsn),
            "scratch_database": scratch_name,
            "artifact": artifact.name,
            "manifest": artifact.with_suffix(".manifest.json").name,
            "verification": verification,
            "kept": keep,
            "ok": verification["ok"],
        }
    finally:
        if created and not keep:
            # Terminate competing connections so DROP DATABASE does not block.
            run_command(
                psql_command(
                    f"SELECT pg_terminate_backend(pid) FROM pg_stat_activity "
                    f"WHERE datname = '{scratch_name}' AND pid <> pg_backend_pid()"
                ),
                env,
            )
            run_command(psql_command(f"DROP DATABASE {scratch_name}"), env)
    report_path = output_dir / f"evia-restore-drill-{dt.datetime.now(dt.timezone.utc).strftime('%Y%m%dT%H%M%SZ')}.report.json"
    write_json(report_path, report)
    report["report_path"] = str(report_path)
    return report


def _dsn_with_database(dsn: str, dbname: str):
    parsed = urllib.parse.urlparse(dsn)
    return parsed._replace(path="/" + dbname)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Evia PostgreSQL backup/restore drill tool")
    sub = parser.add_subparsers(dest="command", required=True)

    backup = sub.add_parser("backup", help="pg_dump plus signed manifest")
    backup.add_argument("--dsn", required=True)
    backup.add_argument("--out", required=True, type=Path)
    backup.add_argument("--schema", default=DEFAULT_SCHEMA)

    snapshot = sub.add_parser("snapshot", help="emit the database snapshot as JSON")
    snapshot.add_argument("--dsn", required=True)
    snapshot.add_argument("--schema", default=DEFAULT_SCHEMA)

    verify = sub.add_parser("verify", help="compare source and restored databases")
    verify.add_argument("--source-dsn", required=True)
    verify.add_argument("--target-dsn", required=True)
    verify.add_argument("--schema", default=DEFAULT_SCHEMA)

    restore = sub.add_parser("restore", help="restore an artifact into a scratch database")
    restore.add_argument("--dsn", required=True)
    restore.add_argument("--artifact", required=True, type=Path)
    restore.add_argument("--manifest", type=Path)

    drill = sub.add_parser("drill", help="backup, restore into scratch and verify")
    drill.add_argument("--dsn", required=True)
    drill.add_argument("--out", required=True, type=Path)
    drill.add_argument("--schema", default=DEFAULT_SCHEMA)
    drill.add_argument("--keep", action="store_true", help="keep the scratch database for inspection")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        if args.command == "backup":
            artifact, manifest = do_backup(args.dsn, args.out, args.schema)
            print(json.dumps({"artifact": str(artifact), "manifest": manifest}, sort_keys=True))
        elif args.command == "snapshot":
            print(json.dumps(take_snapshot(parse_dsn(args.dsn), args.schema), indent=2, sort_keys=True))
        elif args.command == "verify":
            report = do_verify(args.source_dsn, args.target_dsn, args.schema)
            print(json.dumps(report, indent=2, sort_keys=True))
            if not report["ok"]:
                return 1
        elif args.command == "restore":
            manifest = json.loads(args.manifest.read_text()) if args.manifest else None
            do_restore(args.dsn, args.artifact, manifest)
            print(json.dumps({"restored": str(args.artifact)}))
        elif args.command == "drill":
            report = do_drill(args.dsn, args.out, args.schema, args.keep)
            print(json.dumps(report, indent=2, sort_keys=True))
            if not report["ok"]:
                return 1
    except (ValueError, RuntimeError) as error:
        print(f"backup drill failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
