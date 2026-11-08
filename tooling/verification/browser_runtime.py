#!/usr/bin/env python3
"""Prepare a dedicated synthetic browser runtime; never seed facts or readiness.

init DEPS_ENV RUNTIME_ENV derives private runtime settings from deploy/dev-env and
EVIA_DATABASE_URL. prepare DEPS_ENV RUNTIME_ENV explicitly creates the reserved
Kafka topics and applies the checked-in ClickHouse migration chain. run RUNTIME_ENV
-- COMMAND injects settings without sourcing shell code or printing credentials.
The caller owns PostgreSQL migrate/bootstrap, binary builds and process lifecycle.
"""
import hashlib
import os
from pathlib import Path
import re
import secrets
import stat
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
NAMESPACE = re.compile(r"evia_browser_[a-f0-9]{12}\Z")


def fail(message):
    raise SystemExit(message)


def load(path):
    path = Path(path).expanduser().resolve()
    if not path.is_file() or stat.S_IMODE(path.stat().st_mode) & 0o077:
        fail("Use a private environment file with mode 0600.")
    settings = {}
    for line in path.read_text(encoding="utf-8").splitlines():
        if not line or line.startswith("#"):
            continue
        key, separator, value = line.partition("=")
        if not separator or not key.startswith("EVIA_") or not key.isidentifier():
            fail("Invalid private environment file; expected EVIA_NAME=value.")
        settings[key] = value
    return path, settings


def required(settings, key):
    value = settings.get(key)
    if not value:
        fail(f"{key} is required.")
    return value


def initialize(dependency_path, runtime_path):
    _, dependencies = load(dependency_path)
    path = Path(runtime_path).expanduser().resolve()
    if path.is_relative_to(ROOT):
        fail("Runtime credentials must be stored outside the repository.")
    namespace = "evia_browser_" + secrets.token_hex(6)
    password = os.environ.get("EVIA_E2E_PASSWORD") or secrets.token_hex(24)
    settings = {
        "EVIA_DATABASE_URL": required(os.environ, "EVIA_DATABASE_URL"),
        "EVIA_LISTEN": "127.0.0.1:8080",
        "EVIA_PUBLIC_ORIGIN": os.environ.get("EVIA_PUBLIC_ORIGIN", "http://127.0.0.1:5173"),
        "EVIA_ALLOW_INSECURE_HTTP": "true",
        "EVIA_ALLOW_INSECURE_DEPENDENCIES": "true",
        "EVIA_COLLECTION_ENABLED": "true",
        "EVIA_ANALYSIS_ENABLED": "true",
        "EVIA_CURSOR_KEY": os.environ.get("EVIA_CURSOR_KEY") or secrets.token_hex(32),
        "EVIA_SOURCE_SECRET_KEY": os.environ.get("EVIA_SOURCE_SECRET_KEY") or secrets.token_hex(32),
        "EVIA_REDIS_URL": required(dependencies, "EVIA_TEST_REDIS_URL"),
        "EVIA_REDIS_KEY_PREFIX": namespace + ":",
        "EVIA_KAFKA_BROKERS": required(dependencies, "EVIA_TEST_KAFKA_BROKERS"),
        "EVIA_KAFKA_TLS_ENABLED": "false",
        "EVIA_KAFKA_TOPIC": namespace + ".events",
        "EVIA_KAFKA_DEAD_LETTER_TOPIC": namespace + ".quarantine",
        "EVIA_KAFKA_PROBE_TOPIC": namespace + ".readiness",
        "EVIA_KAFKA_GROUP": namespace + "-materializer",
        "EVIA_CLICKHOUSE_URL": required(dependencies, "EVIA_TEST_CLICKHOUSE_URL"),
        "EVIA_CLICKHOUSE_DATABASE": namespace,
        "EVIA_CLICKHOUSE_USERNAME": required(dependencies, "EVIA_TEST_CLICKHOUSE_USERNAME"),
        "EVIA_CLICKHOUSE_PASSWORD": required(dependencies, "EVIA_TEST_CLICKHOUSE_PASSWORD"),
        "EVIA_BOOTSTRAP_USERNAME": os.environ.get("EVIA_E2E_USERNAME", "ci-admin"),
        "EVIA_BOOTSTRAP_DISPLAY_NAME": "Synthetic browser administrator",
        "EVIA_BOOTSTRAP_PASSWORD": password,
        "EVIA_E2E_USERNAME": os.environ.get("EVIA_E2E_USERNAME", "ci-admin"),
        "EVIA_E2E_PASSWORD": password,
        "EVIA_E2E_FULL_STACK": "1",
    }
    if any("\n" in value or "\r" in value for value in settings.values()):
        fail("Environment values must not contain line breaks.")
    descriptor = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    with os.fdopen(descriptor, "w") as stream:
        for key, value in settings.items():
            stream.write(f"{key}={value}\n")
    # Mask generated values before any later workflow command can display them.
    if os.environ.get("GITHUB_ACTIONS") == "true":
        for key in ("EVIA_E2E_PASSWORD", "EVIA_CURSOR_KEY", "EVIA_SOURCE_SECRET_KEY",
                    "EVIA_CLICKHOUSE_PASSWORD", "EVIA_REDIS_URL", "EVIA_DATABASE_URL"):
            print("::add-mask::" + settings[key])
    print(f"Private browser runtime created at {path}; values omitted.")


def compose(dependency_path, dependencies, arguments, payload=None):
    environment = dict(os.environ)
    environment.update(dependencies)
    result = subprocess.run([
        "docker", "compose", "--project-name", "evia-pipeline-dev",
        "--env-file", str(dependency_path), "-f", str(ROOT / "deploy/compose.dev.yaml"),
        "--profile", "pipeline", "exec", "-T", *arguments,
    ], input=payload, env=environment, text=True, capture_output=True)
    if result.returncode:
        # Dependency errors can include authenticated URLs or request content.
        fail("Browser dependency preparation failed; inspect private dependency diagnostics.")
    return result.stdout


def prepare(dependency_path, runtime_path):
    dependency_path, dependencies = load(dependency_path)
    _, runtime = load(runtime_path)
    database = required(runtime, "EVIA_CLICKHOUSE_DATABASE")
    for runtime_key, dependency_key in (
        ("EVIA_KAFKA_BROKERS", "EVIA_TEST_KAFKA_BROKERS"),
        ("EVIA_CLICKHOUSE_URL", "EVIA_TEST_CLICKHOUSE_URL"),
        ("EVIA_CLICKHOUSE_USERNAME", "EVIA_TEST_CLICKHOUSE_USERNAME"),
        ("EVIA_CLICKHOUSE_PASSWORD", "EVIA_TEST_CLICKHOUSE_PASSWORD"),
        ("EVIA_REDIS_URL", "EVIA_TEST_REDIS_URL"),
    ):
        if required(runtime, runtime_key) != required(dependencies, dependency_key):
            fail("Runtime and dependency configuration must describe the same services.")
    for key, expected in (
        ("EVIA_TEST_KAFKA_BROKERS", "127.0.0.1:19092"),
        ("EVIA_TEST_CLICKHOUSE_URL", "http://127.0.0.1:18123"),
    ):
        if dependencies.get(key) != expected:
            fail("Preparation supports only the loopback deploy/dev-env dependency profile.")
    if not NAMESPACE.fullmatch(database):
        fail("Preparation is restricted to the helper's dedicated synthetic browser namespace.")
    for key, expected in (
        ("EVIA_KAFKA_TOPIC", database + ".events"),
        ("EVIA_KAFKA_DEAD_LETTER_TOPIC", database + ".quarantine"),
        ("EVIA_KAFKA_PROBE_TOPIC", database + ".readiness"),
    ):
        if runtime.get(key) != expected:
            fail("Runtime topics must belong to its dedicated synthetic browser namespace.")
        compose(dependency_path, dependencies, [
            "kafka", "/opt/kafka/bin/kafka-topics.sh", "--bootstrap-server", "localhost:9092",
            "--create", "--if-not-exists", "--topic", expected, "--partitions", "1",
            "--replication-factor", "1", "--config", "min.insync.replicas=1",
            "--config", "retention.ms=3600000", "--config", "retention.bytes=67108864",
        ])
    print("Dedicated browser Kafka topics provisioned.")

    def sql(statement, selected="default"):
        return compose(dependency_path, dependencies, [
            "clickhouse", "/bin/sh", "-ec",
            'exec clickhouse-client --host 127.0.0.1 --user "$CLICKHOUSE_USER" '
            '--password "$CLICKHOUSE_PASSWORD" --database "$1" --multiquery',
            "browser-runtime", selected,
        ], statement)

    sql(f"CREATE DATABASE IF NOT EXISTS {database};")
    sql("CREATE TABLE IF NOT EXISTS browser_migrations (name String, checksum FixedString(64)) "
        "ENGINE = MergeTree ORDER BY name;", database)
    applied = dict(line.split("\t") for line in sql(
        "SELECT name, checksum FROM browser_migrations FORMAT TabSeparated;", database).splitlines())
    for migration in sorted((ROOT / "deploy/clickhouse").glob("*.sql")):
        contents = migration.read_text(encoding="utf-8")
        checksum = hashlib.sha256(contents.encode()).hexdigest()
        if migration.name in applied:
            if applied[migration.name] != checksum:
                fail("An applied browser ClickHouse migration changed; use a fresh test namespace.")
            continue
        # Initial DDL predates a ledger. These guarded additions allow an interrupted
        # explicit preparation to resume only within this disposable test namespace.
        if migration.name in ("001_event_facts.sql", "002_readiness.sql"):
            contents = contents.replace("CREATE TABLE ", "CREATE TABLE IF NOT EXISTS ")
            contents = contents.replace("CREATE VIEW ", "CREATE VIEW IF NOT EXISTS ")
        sql(contents, database)
        sql(f"INSERT INTO browser_migrations VALUES ('{migration.name}', '{checksum}');", database)
        print("Applied browser ClickHouse migration " + migration.name + ".")
    print("Browser ClickHouse migration chain verified; no event or readiness data seeded.")


def main():
    if len(sys.argv) < 4:
        fail("Usage: browser_runtime.py init|prepare DEPS_ENV RUNTIME_ENV | run RUNTIME_ENV -- COMMAND ...")
    action = sys.argv[1]
    if action == "init" and len(sys.argv) == 4:
        initialize(*sys.argv[2:])
    elif action == "prepare" and len(sys.argv) == 4:
        prepare(*sys.argv[2:])
    elif action == "run":
        _, runtime = load(sys.argv[2])
        command = sys.argv[3:]
        if command[0] == "--":
            command = command[1:]
        if not command:
            fail("run requires a command.")
        environment = dict(os.environ)
        environment.update(runtime)
        raise SystemExit(subprocess.run(command, env=environment, cwd=ROOT).returncode)
    else:
        fail("Unknown browser runtime action or invalid arguments.")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        fail(f"Browser runtime command failed ({type(error).__name__}); values omitted.")
