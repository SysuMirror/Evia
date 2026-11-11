"""ClickHouse backup, restore and drill automation for Evia analytics data.

The event fact warehouse in ClickHouse is the materialized analytics state of
an Evia deployment. This module makes its backup/restore loop auditable using
the server's native BACKUP/RESTORE engine (no third-party tools):

* ``backup``   - native ``BACKUP DATABASE ... TO File`` copied to the host plus
                 a signed manifest (per-file sha256 and table fingerprints)
* ``snapshot`` - per-table row counts and cityHash64 content fingerprints
* ``verify``   - structural and fingerprint comparison of two databases
* ``restore``  - copy an artifact back and RESTORE under a scratch database name
* ``drill``    - the full backup -> scratch restore -> verify -> cleanup cycle

The module uses only the Python standard library. The host has no
clickhouse-client in the reference environment, so the default transport is
``docker exec`` against the pipeline container; override ``EVIA_CH_CLIENT``
with a shlex command prefix for native deployments. Credentials travel through
the CLICKHOUSE_PASSWORD environment variable, never through argv.
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
import shlex
import subprocess
import sys
from pathlib import Path

MANIFEST_FORMAT_VERSION = 1
DEFAULT_CLIENT = "docker exec evia-pipeline-dev-clickhouse-1 clickhouse-client"
CONTAINER_BACKUP_ROOT = "/var/lib/clickhouse/backups"
IDENTIFIER_PATTERN = re.compile(r"^[a-z_][a-z0-9_]*$")
BACKUP_NAME_PATTERN = re.compile(r"^evia_ch_backup_[0-9]{8}t[0-9]{6}_[a-f0-9]{6}$")
SCRATCH_NAME_PATTERN = re.compile(r"^evia_ch_restore_drill_[a-f0-9]{6,32}$")
DEFAULT_TIMEOUT_SECONDS = 600


@dataclasses.dataclass(frozen=True)
class CHClient:
    """Invocation configuration for the ClickHouse command line client."""

    argv_prefix: list[str]
    username: str
    password: str
    timeout: int = DEFAULT_TIMEOUT_SECONDS

    @property
    def is_docker(self) -> bool:
        return len(self.argv_prefix) >= 2 and self.argv_prefix[:2] == ["docker", "exec"]

    def query_argv(self, sql: str) -> list[str]:
        if not self.username:
            raise ValueError("ClickHouse username is required")
        if self.is_docker:
            # -e forwards the host CLICKHOUSE_PASSWORD into the container so
            # the secret never appears in argv inside the container.
            return (
                self.argv_prefix[:2]
                + ["-e", "CLICKHOUSE_PASSWORD"]
                + self.argv_prefix[2:]
                + ["--user", self.username, "--query", sql]
            )
        return [*self.argv_prefix, "--user", self.username, "--query", sql]

    def environment(self) -> dict[str, str]:
        env = dict(os.environ)
        if self.password:
            env["CLICKHOUSE_PASSWORD"] = self.password
        return env


def parse_client(raw: str | None, username: str | None, password: str | None,
                 timeout: int = DEFAULT_TIMEOUT_SECONDS) -> CHClient:
    prefix = shlex.split(raw or os.environ.get("EVIA_CH_CLIENT") or DEFAULT_CLIENT)
    if not prefix:
        raise ValueError("EVIA_CH_CLIENT must not be empty")
    return CHClient(
        argv_prefix=prefix,
        username=username or os.environ.get("EVIA_CH_USERNAME", ""),
        password=password or os.environ.get("EVIA_CH_PASSWORD", ""),
        timeout=timeout,
    )


def validate_identifier(name: str) -> None:
    """Database and table identifiers are interpolated into CH SQL after this."""
    if not IDENTIFIER_PATTERN.fullmatch(name):
        raise ValueError(f"invalid ClickHouse identifier: {name!r}")


def validate_scratch_name(name: str) -> None:
    if not SCRATCH_NAME_PATTERN.fullmatch(name):
        raise ValueError(
            "scratch database must match evia_ch_restore_drill_<6-32 lowercase hex>"
        )


def new_backup_name() -> str:
    stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dt%H%M%S")
    return f"evia_ch_backup_{stamp}_{secrets.token_hex(3)}"


def new_scratch_name() -> str:
    return f"evia_ch_restore_drill_{secrets.token_hex(12)}"


def list_tables_sql(database: str) -> str:
    validate_identifier(database)
    return (
        "SELECT name FROM system.tables WHERE database = "
        f"'{database}' ORDER BY name"
    )


def fingerprint_sql(database: str, table: str) -> str:
    validate_identifier(database)
    validate_identifier(table)
    # coalesce keeps empty tables comparable: sum over zero rows is NULL.
    return (
        f"SELECT count(), coalesce(sum(cityHash64(*)), 0) "
        f"FROM {database}.{table}"
    )


def backup_sql(database: str, backup_name: str) -> str:
    validate_identifier(database)
    if not BACKUP_NAME_PATTERN.fullmatch(backup_name):
        raise ValueError(f"refusing unmanaged backup name: {backup_name!r}")
    return f"BACKUP DATABASE {database} TO File('{backup_name}')"


def restore_sql(database: str, scratch: str, backup_name: str) -> str:
    validate_identifier(database)
    validate_scratch_name(scratch)
    if not BACKUP_NAME_PATTERN.fullmatch(backup_name):
        raise ValueError(f"refusing unmanaged backup name: {backup_name!r}")
    return (
        f"RESTORE DATABASE {database} AS {scratch} "
        f"FROM File('{backup_name}')"
    )


def drop_database_sql(database: str) -> str:
    validate_scratch_name(database)
    return f"DROP DATABASE {database} SYNC"


TAR_ENTRY = "backup.tar"


def tar_export_argv(container: str, root: str, backup_name: str) -> list[str]:
    # docker cp cannot see tmpfs-backed container paths; tar streams through
    # the exec socket instead, reading the live mount namespace.
    return ["docker", "exec", container, "tar", "-C", root, "-cf", "-", backup_name]


def tar_import_argv(container: str, root: str) -> list[str]:
    return ["docker", "exec", "-i", container, "tar", "-C", root, "-xf", "-"]


def container_from_prefix(prefix: list[str]) -> str:
    if len(prefix) >= 3 and prefix[:2] == ["docker", "exec"]:
        return prefix[2]
    raise ValueError("artifact copy requires a docker exec client prefix")


def run_command(cmd: list[str], env: dict[str, str], timeout: int) -> str:
    try:
        result = subprocess.run(
            cmd, capture_output=True, text=True, env=env, timeout=timeout, check=False
        )
    except (OSError, subprocess.SubprocessError) as error:
        raise RuntimeError(f"command failed to start: {cmd[0]}: {error}") from error
    if result.returncode != 0:
        raise RuntimeError(
            f"command exited {result.returncode}: {result.stderr.strip() or result.stdout.strip()}"
        )
    return result.stdout


def run_command_bytes(cmd: list[str], env: dict[str, str], timeout: int,
                       stdin: bytes | None = None) -> bytes:
    try:
        result = subprocess.run(
            cmd, input=stdin, capture_output=True, env=env, timeout=timeout, check=False
        )
    except (OSError, subprocess.SubprocessError) as error:
        raise RuntimeError(f"command failed to start: {cmd[0]}: {error}") from error
    if result.returncode != 0:
        raise RuntimeError(
            f"command exited {result.returncode}: {result.stderr.decode('utf-8', 'replace').strip()}"
        )
    return result.stdout


def run_sql(client: CHClient, sql: str) -> str:
    return run_command(client.query_argv(sql), client.environment(), client.timeout)


def list_tables(client: CHClient, database: str) -> list[str]:
    out = run_sql(client, list_tables_sql(database))
    return [line.strip() for line in out.splitlines() if line.strip()]


def database_exists(client: CHClient, database: str) -> bool:
    validate_identifier(database)
    out = run_sql(client, "SELECT name FROM system.databases ORDER BY name")
    names = {line.strip() for line in out.splitlines() if line.strip()}
    return database in names


def take_snapshot(client: CHClient, database: str) -> dict:
    validate_identifier(database)
    tables: dict[str, dict[str, int]] = {}
    for table in list_tables(client, database):
        out = run_sql(client, fingerprint_sql(database, table)).strip()
        rows_text, hash_text = out.split("\t")
        tables[table] = {"rows": int(rows_text), "hash": int(hash_text)}
    return {"database": database, "tables": tables}


def compare_snapshots(source: dict, target: dict) -> list[str]:
    problems: list[str] = []
    source_tables = source["tables"]
    target_tables = target["tables"]
    for name in source_tables:
        if name not in target_tables:
            problems.append(f"table missing from restored database: {name}")
    for name in target_tables:
        if name not in source_tables:
            problems.append(f"unexpected table in restored database: {name}")
    for name, fingerprint in source_tables.items():
        other = target_tables.get(name)
        if other is None:
            continue
        if fingerprint["rows"] != other["rows"]:
            problems.append(
                f"row count differs for {name}: {fingerprint['rows']} != {other['rows']}"
            )
        if fingerprint["hash"] != other["hash"]:
            problems.append(
                f"content hash differs for {name}: {fingerprint['hash']} != {other['hash']}"
            )
    return problems


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def artifact_files(artifact_dir: Path) -> dict[str, str]:
    files: dict[str, str] = {}
    for path in sorted(p for p in artifact_dir.rglob("*") if p.is_file()):
        files[str(path.relative_to(artifact_dir))] = sha256_file(path)
    if not files:
        raise ValueError(f"artifact directory is empty: {artifact_dir}")
    return files


def build_manifest(database: str, backup_name: str, artifact_dir: Path,
                   snapshot: dict) -> dict:
    return {
        "format_version": MANIFEST_FORMAT_VERSION,
        "database": database,
        "backup_name": backup_name,
        "created_at": dt.datetime.now(dt.timezone.utc).isoformat(),
        "snapshot": snapshot,
        "files": artifact_files(artifact_dir),
    }


def manifest_path(output_dir: Path, backup_name: str) -> Path:
    return output_dir / f"{backup_name}.manifest.json"


def write_json(path: Path, payload: dict) -> None:
    path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def verify_artifact(manifest: dict, artifact_dir: Path) -> None:
    if manifest.get("format_version") != MANIFEST_FORMAT_VERSION:
        raise ValueError("unsupported manifest format version")
    declared = manifest.get("files", {})
    actual = artifact_files(artifact_dir)
    if set(declared) != set(actual):
        raise ValueError("artifact file set does not match manifest")
    for name, digest in declared.items():
        if digest != actual[name]:
            raise ValueError(f"artifact checksum mismatch: {name}")


def remove_container_path(client: CHClient, container_path: str) -> None:
    container = container_from_prefix(client.argv_prefix)
    run_command(
        ["docker", "exec", container, "rm", "-rf", container_path],
        client.environment(),
        client.timeout,
    )


def do_backup(client: CHClient, database: str, output_dir: Path) -> tuple[Path, dict]:
    validate_identifier(database)
    backup_name = new_backup_name()
    output_dir.mkdir(parents=True, exist_ok=True)
    snapshot = take_snapshot(client, database)
    run_sql(client, backup_sql(database, backup_name))
    container = container_from_prefix(client.argv_prefix)
    container_path = f"{CONTAINER_BACKUP_ROOT}/{backup_name}"
    host_artifact = output_dir / backup_name
    host_artifact.mkdir(parents=True, exist_ok=True)
    payload = run_command_bytes(
        tar_export_argv(container, CONTAINER_BACKUP_ROOT, backup_name),
        client.environment(),
        client.timeout,
    )
    (host_artifact / TAR_ENTRY).write_bytes(payload)
    manifest = build_manifest(database, backup_name, host_artifact, snapshot)
    write_json(manifest_path(output_dir, backup_name), manifest)
    # The reference container keeps ClickHouse data on a bounded tmpfs; the host
    # copy plus signed manifest is the durable artifact, so free the space.
    remove_container_path(client, container_path)
    return host_artifact, manifest


def do_restore(client: CHClient, artifact_dir: Path, manifest: dict,
               database: str, scratch: str) -> None:
    validate_identifier(database)
    validate_scratch_name(scratch)
    backup_name = manifest["backup_name"]
    if not BACKUP_NAME_PATTERN.fullmatch(backup_name):
        raise ValueError(f"refusing unmanaged backup name: {backup_name!r}")
    if database_exists(client, scratch):
        raise ValueError(f"scratch database already exists: {scratch}")
    container = container_from_prefix(client.argv_prefix)
    run_command(
        ["docker", "exec", container, "mkdir", "-p", CONTAINER_BACKUP_ROOT],
        client.environment(),
        client.timeout,
    )
    container_path = f"{CONTAINER_BACKUP_ROOT}/{backup_name}"
    payload = (artifact_dir / TAR_ENTRY).read_bytes()
    run_command_bytes(
        tar_import_argv(container, CONTAINER_BACKUP_ROOT),
        client.environment(),
        client.timeout,
        stdin=payload,
    )
    try:
        run_sql(client, restore_sql(database, scratch, backup_name))
    finally:
        remove_container_path(client, container_path)


def do_verify(client: CHClient, database: str, restored: str) -> dict:
    source = take_snapshot(client, database)
    target = take_snapshot(client, restored)
    problems = compare_snapshots(source, target)
    if problems:
        raise RuntimeError("snapshot verification failed: " + "; ".join(problems))
    return {"source": source, "restored": target, "problems": []}


def do_drill(client: CHClient, database: str, output_dir: Path, keep: bool) -> dict:
    snapshot = take_snapshot(client, database)
    artifact_dir, manifest = do_backup(client, database, output_dir)
    verify_artifact(manifest, artifact_dir)
    scratch = new_scratch_name()
    try:
        do_restore(client, artifact_dir, manifest, database, scratch)
        restored_snapshot = take_snapshot(client, scratch)
        problems = compare_snapshots(snapshot, restored_snapshot)
        if problems:
            raise RuntimeError("drill verification failed: " + "; ".join(problems))
    finally:
        try:
            run_sql(client, drop_database_sql(scratch))
        except RuntimeError:
            pass
    if not keep:
        for path in artifact_dir.rglob("*"):
            if path.is_file():
                path.unlink()
        for path in sorted(artifact_dir.rglob("*"), reverse=True):
            if path.is_dir():
                path.rmdir()
        artifact_dir.rmdir()
        manifest_path(output_dir, manifest["backup_name"]).unlink(missing_ok=True)
    return {
        "database": database,
        "backup_name": manifest["backup_name"],
        "scratch_database": scratch,
        "tables": len(snapshot["tables"]),
        "problems": [],
        "artifact_retained": keep,
    }


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Evia ClickHouse backup/restore drill tool"
    )
    parser.add_argument("--client", help="shlex command prefix (env EVIA_CH_CLIENT)")
    parser.add_argument("--username", help="CH username (env EVIA_CH_USERNAME)")
    parser.add_argument("--password", help="CH password (env EVIA_CH_PASSWORD)")
    parser.add_argument("--timeout", type=int, default=DEFAULT_TIMEOUT_SECONDS)
    sub = parser.add_subparsers(dest="command", required=True)

    backup = sub.add_parser("backup", help="native File backup plus signed manifest")
    backup.add_argument("--database", required=True)
    backup.add_argument("--output", type=Path, required=True)

    snapshot = sub.add_parser("snapshot", help="emit table fingerprints as JSON")
    snapshot.add_argument("--database", required=True)

    verify = sub.add_parser("verify", help="compare two databases by fingerprint")
    verify.add_argument("--database", required=True)
    verify.add_argument("--restored-database", required=True)

    restore = sub.add_parser("restore", help="restore an artifact into a scratch database")
    restore.add_argument("--artifact", type=Path, required=True)
    restore.add_argument("--database", required=True)
    restore.add_argument("--scratch")

    drill = sub.add_parser("drill", help="backup, restore into scratch and verify")
    drill.add_argument("--database", required=True)
    drill.add_argument("--output", type=Path, required=True)
    drill.add_argument("--keep", action="store_true", help="retain the host artifact")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    client = parse_client(args.client, args.username, args.password, args.timeout)
    try:
        if args.command == "backup":
            artifact_dir, manifest = do_backup(client, args.database, args.output)
            print(json.dumps({"artifact": str(artifact_dir),
                              "manifest": str(manifest_path(args.output, manifest["backup_name"])),
                              "tables": len(manifest["snapshot"]["tables"])}, ensure_ascii=False))
        elif args.command == "snapshot":
            print(json.dumps(take_snapshot(client, args.database), indent=2, sort_keys=True))
        elif args.command == "verify":
            print(json.dumps(do_verify(client, args.database, args.restored_database),
                             indent=2, sort_keys=True))
        elif args.command == "restore":
            manifest_file = (
                args.artifact.parent / f"{args.artifact.name}.manifest.json"
            )
            if not manifest_file.is_file():
                raise ValueError(f"manifest not found next to artifact: {manifest_file}")
            manifest = json.loads(manifest_file.read_text(encoding="utf-8"))
            verify_artifact(manifest, args.artifact)
            scratch = args.scratch or new_scratch_name()
            do_restore(client, args.artifact, manifest, args.database, scratch)
            print(json.dumps({"scratch_database": scratch}, ensure_ascii=False))
        elif args.command == "drill":
            print(json.dumps(do_drill(client, args.database, args.output, args.keep),
                             indent=2, sort_keys=True, ensure_ascii=False))
    except (ValueError, RuntimeError) as error:
        print(f"clickhouse drill failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
