"""Offsite replication of verified backup artifacts into S3-compatible storage.

The drill scheduler proves backups restore correctly on the local host; this
module proves they also exist outside it. For every managed artifact it:

1. uploads the object with its sha256 as the SigV4 payload hash,
2. downloads the object back into a scratch file and compares the sha256,
3. records the verified copy in a durable state file,
4. prunes remote objects beyond the same retention window as local storage.

Everything is idempotent: re-running with unchanged artifacts performs no
network writes, and a crashed run resumes from the state file rather than
re-uploading verified objects. Credential names, never values, live in the
config; failure reports redact secret material.
"""
from __future__ import annotations

import argparse
import dataclasses
import json
import os
import re
import sys
import tempfile
from datetime import datetime, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import objectstore  # noqa: E402
import schedule  # noqa: E402

REPLICATION_FORMAT_VERSION = 1
STATE_FORMAT_VERSION = 1
MAX_SOURCES = 8
VERIFY_DOWNLOAD = "download"
VERIFY_HEAD = "head"
VERIFY_MODES = (VERIFY_DOWNLOAD, VERIFY_HEAD)


class ReplicationError(ValueError):
    """Raised for invalid configuration or unsafe replication state."""


def utc_stamp() -> str:
    return datetime.now(timezone.utc).isoformat()


@dataclasses.dataclass(frozen=True)
class ReplicationSource:
    name: str
    directory: Path
    patterns: tuple[re.Pattern[str], ...]
    prefix: str
    retention_keep: int
    verify: str

    def managed_files(self) -> list[Path]:
        if not self.directory.exists():
            return []
        entries = [
            path
            for path in self.directory.iterdir()
            if path.is_file() and any(pattern.fullmatch(path.name) for pattern in self.patterns)
        ]
        return sorted(entries, key=lambda path: path.name, reverse=True)


def source_patterns(kind: str) -> tuple[re.Pattern[str], ...]:
    if kind == "postgres":
        return schedule.PG_DUMP_RE, schedule.PG_MANIFEST_RE, schedule.PG_REPORT_RE
    if kind == "clickhouse":
        # ClickHouse backup directories are replicated as their tar payload
        # plus manifest; directory names themselves are not object keys.
        return schedule.CH_MANIFEST_RE, re.compile(r"^backup\.tar$")
    raise ReplicationError(f"unknown source kind: {kind}")


def parse_source(raw: dict) -> ReplicationSource:
    name = raw.get("name")
    if not isinstance(name, str) or not schedule.TARGET_NAME_RE.fullmatch(name):
        raise ReplicationError(f"invalid source name: {name!r}")
    kind = raw.get("kind")
    if kind not in schedule.KINDS:
        raise ReplicationError(f"source {name}: kind must be one of {schedule.KINDS}")
    directory = raw.get("directory")
    if not isinstance(directory, str) or not directory:
        raise ReplicationError(f"source {name}: directory is required")
    prefix = raw.get("prefix", name + "/")
    if not isinstance(prefix, str) or not re.fullmatch(r"[A-Za-z0-9._/-]{1,256}", prefix):
        raise ReplicationError(f"source {name}: invalid bucket prefix")
    keep = raw.get("retention_keep", schedule.DEFAULT_RETENTION_KEEP)
    if not isinstance(keep, int) or not schedule.MIN_KEEP <= keep <= schedule.MAX_KEEP:
        raise ReplicationError(f"source {name}: retention_keep out of range")
    verify = raw.get("verify", VERIFY_DOWNLOAD)
    if verify not in VERIFY_MODES:
        raise ReplicationError(f"source {name}: verify must be one of {VERIFY_MODES}")
    return ReplicationSource(
        name=name,
        directory=Path(directory),
        patterns=source_patterns(kind),
        prefix=prefix,
        retention_keep=keep,
        verify=verify,
    )


def load_config(path: Path) -> tuple[objectstore.S3Config, list[ReplicationSource], Path]:
    try:
        document = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as error:
        raise ReplicationError(f"config not found: {path}") from error
    except json.JSONDecodeError as error:
        raise ReplicationError(f"config is not valid JSON: {error}") from error
    if document.get("version") != REPLICATION_FORMAT_VERSION:
        raise ReplicationError(f"unsupported config version: {document.get('version')!r}")
    raw_store = document.get("store")
    if not isinstance(raw_store, dict):
        raise ReplicationError("store section is required")
    try:
        store = objectstore.S3Config(
            endpoint=raw_store["endpoint"],
            region=raw_store["region"],
            bucket=raw_store["bucket"],
            access_key_env=raw_store["access_key_env"],
            secret_key_env=raw_store["secret_key_env"],
            path_style=bool(raw_store.get("path_style", True)),
            insecure_http=bool(raw_store.get("insecure_http", False)),
            timeout_seconds=int(raw_store.get("timeout_seconds", 60)),
        )
    except KeyError as error:
        raise ReplicationError(f"store is missing field {error}") from error
    except TypeError as error:
        raise ReplicationError(f"store has malformed fields: {error}") from error
    raw_sources = document.get("sources")
    if not isinstance(raw_sources, list) or not raw_sources:
        raise ReplicationError("sources must be a non-empty list")
    if len(raw_sources) > MAX_SOURCES:
        raise ReplicationError(f"at most {MAX_SOURCES} sources are supported")
    sources = [parse_source(item) for item in raw_sources]
    if len({source.name for source in sources}) != len(sources):
        raise ReplicationError("source names must be unique")
    state_dir = document.get("state_dir")
    if not isinstance(state_dir, str) or not state_dir.strip():
        raise ReplicationError("state_dir must be a non-empty string")
    return store, sources, Path(state_dir)


def load_state(path: Path) -> dict:
    if not path.exists():
        return {"version": STATE_FORMAT_VERSION, "objects": {}}
    document = json.loads(path.read_text(encoding="utf-8"))
    if document.get("version") != STATE_FORMAT_VERSION or not isinstance(document.get("objects"), dict):
        raise ReplicationError(f"state file {path} is malformed; refusing to reset replication state")
    return document


def write_state_atomic(path: Path, state: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + ".tmp")
    tmp.write_text(json.dumps(state, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    os.replace(tmp, path)


def object_key(source: ReplicationSource, path: Path) -> str:
    return source.prefix.rstrip("/") + "/" + path.name


def verify_upload(client: objectstore.S3Client, source: ReplicationSource, key: str,
                 local_path: Path, local_sha256: str) -> None:
    if source.verify == VERIFY_HEAD:
        client.head_object(key)
        return
    with tempfile.TemporaryDirectory(prefix="evia-repl-") as tmpdir:
        scratch = Path(tmpdir) / local_path.name
        client.download_object(key, scratch, expected_sha256=local_sha256)


def replicate_source(
    client: objectstore.S3Client,
    source: ReplicationSource,
    state: dict,
    objects: dict,
) -> dict:
    report = {"source": source.name, "uploaded": [], "verified": [], "pruned": [], "skipped": []}
    # Retention is applied per filename pattern, mirroring the local scheduler:
    # each pattern keeps its own newest retention_keep entries rather than
    # sharing one counter across dumps and manifests.
    keep_names: set[str] = set()
    for pattern in source.patterns:
        matched = sorted(
            (path for path in source.directory.iterdir() if path.is_file() and pattern.fullmatch(path.name)),
            key=lambda path: path.name,
            reverse=True,
        )
        keep_names.update(path.name for path in matched[: source.retention_keep])
    files = source.managed_files()
    for path in files:
        key = object_key(source, path)
        local_sha = objectstore.sha256_file(path)
        record = objects.get(key)
        if record and record.get("sha256") == local_sha and client.object_exists(key):
            report["skipped"].append(key)
            continue
        client.put_object(key, path)
        verify_upload(client, source, key, path, local_sha)
        objects[key] = {
            "sha256": local_sha,
            "size": path.stat().st_size,
            "source": source.name,
            "replicated_at": utc_stamp(),
        }
        report["uploaded"].append(key)
        report["verified"].append(key)

    # Remote retention: anything outside the local keep window is deleted, and
    # state for vanished local files is dropped so re-created names re-replicate.
    remote = {item["key"]: item for item in client.list_objects(source.prefix)}
    for key in sorted(remote):
        leaf = key.rsplit("/", 1)[-1]
        if leaf not in keep_names:
            client.delete_object(key)
            objects.pop(key, None)
            report["pruned"].append(key)
    for stale in [key for key in objects if key.startswith(source.prefix) and
                  key.rsplit("/", 1)[-1] not in keep_names]:
        objects.pop(stale, None)
    state["last_run_at"] = utc_stamp()
    return report


def run_once(config_path: Path) -> int:
    store_config, sources, state_dir = load_config(config_path)
    state_dir.mkdir(parents=True, exist_ok=True)
    state_path = state_dir / "replication-state.json"
    state = load_state(state_path)
    objects = state["objects"]
    client = objectstore.S3Client(store_config)
    client.ensure_bucket()
    run_report = {"run_at": utc_stamp(), "bucket": store_config.bucket, "sources": []}
    ok = True
    for source in sources:
        try:
            run_report["sources"].append(replicate_source(client, source, state, objects))
        except Exception as error:  # noqa: BLE001 - every source failure must be reported
            secret = os.environ.get(store_config.secret_key_env, "")
            message = str(error).replace(secret, "***") if secret else str(error)
            run_report["sources"].append({"source": source.name, "error": message})
            ok = False
        finally:
            write_state_atomic(state_path, state)
    report_path = state_dir / f"replication-run-{datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ')}.json"
    write_state_atomic(report_path, run_report)
    print(json.dumps(run_report, indent=2, sort_keys=True, ensure_ascii=False))
    return 0 if ok else 1


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Evia offsite backup replication")
    sub = parser.add_subparsers(dest="command", required=True)
    validate = sub.add_parser("validate", help="validate config and credentials without uploading")
    validate.add_argument("--config", required=True, type=Path)
    run = sub.add_parser("run-once", help="replicate every source into the object store once")
    run.add_argument("--config", required=True, type=Path)
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        store_config, _, _ = load_config(args.config)
        if args.command == "validate":
            store_config.credentials()
            print(json.dumps({"ok": True, "bucket": store_config.bucket}))
            return 0
        return run_once(args.config)
    except ReplicationError as error:
        print(f"backup replication failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
