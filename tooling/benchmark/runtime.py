#!/usr/bin/env python3
"""Explicit local benchmark provisioning, isolated from development services.

init DEPS_ENV RUNTIME_ENV creates private settings. prepare creates only benchmark
topics and applies the ClickHouse migration chain. run injects private settings.
Compose startup, PostgreSQL migrate/bootstrap and process lifecycle stay explicit.
"""
import hashlib
import importlib.util
import os
from pathlib import Path
import re
import secrets
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location(
    "private_runtime", ROOT / "tooling/verification/browser_runtime.py")
private_runtime = importlib.util.module_from_spec(spec)
spec.loader.exec_module(private_runtime)
load = private_runtime.load
fail = private_runtime.fail
NAMESPACE = re.compile(r"evia_bench_[a-f0-9]{12}\Z")


def write_private(path, settings):
    path = Path(path).expanduser().resolve()
    if path.is_relative_to(ROOT):
        fail("Benchmark credentials must be stored outside the repository.")
    if any("\n" in value or "\r" in value for value in settings.values()):
        fail("Environment values must not contain line breaks.")
    descriptor = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    with os.fdopen(descriptor, "w") as stream:
        stream.write("".join(f"{key}={value}\n" for key, value in settings.items()))


def initialize(dependency_path, runtime_path):
    if Path(dependency_path).exists() or Path(runtime_path).exists():
        fail("Use fresh private file paths; existing credentials are never replaced.")
    subprocess.run([sys.executable, str(ROOT / "deploy/dev-env"), "init", dependency_path], check=True)
    path, dependencies = load(dependency_path)
    dependencies.update(EVIA_TEST_KAFKA_BROKERS="127.0.0.1:19093",
                        EVIA_TEST_CLICKHOUSE_URL="http://127.0.0.1:18124")
    dependencies["EVIA_TEST_REDIS_URL"] = dependencies["EVIA_TEST_REDIS_URL"].replace(":16379/", ":16380/")
    path.write_text("".join(f"{key}={value}\n" for key, value in dependencies.items()))
    namespace = "evia_bench_" + secrets.token_hex(6)
    password = secrets.token_hex(24)
    settings = {
        "EVIA_DATABASE_URL": "postgres://evia:" + dependencies["EVIA_POSTGRES_PASSWORD"]
                             + "@127.0.0.1:55433/evia?sslmode=disable",
        "EVIA_LISTEN": "127.0.0.1:8081",
        "EVIA_PUBLIC_ORIGIN": "http://127.0.0.1:5173",
        "EVIA_ALLOW_INSECURE_HTTP": "true", "EVIA_ALLOW_INSECURE_DEPENDENCIES": "true",
        "EVIA_COLLECTION_ENABLED": "true", "EVIA_ANALYSIS_ENABLED": "true",
        "EVIA_CURSOR_KEY": secrets.token_hex(32), "EVIA_SOURCE_SECRET_KEY": secrets.token_hex(32),
        "EVIA_REDIS_URL": dependencies["EVIA_TEST_REDIS_URL"], "EVIA_REDIS_KEY_PREFIX": namespace + ":",
        "EVIA_KAFKA_BROKERS": dependencies["EVIA_TEST_KAFKA_BROKERS"], "EVIA_KAFKA_TLS_ENABLED": "false",
        "EVIA_KAFKA_TOPIC": namespace + ".events", "EVIA_KAFKA_DEAD_LETTER_TOPIC": namespace + ".quarantine",
        "EVIA_KAFKA_PROBE_TOPIC": namespace + ".readiness", "EVIA_KAFKA_GROUP": namespace + "-materializer",
        "EVIA_CLICKHOUSE_DATABASE": namespace,
        "EVIA_CLICKHOUSE_URL": dependencies["EVIA_TEST_CLICKHOUSE_URL"],
        "EVIA_CLICKHOUSE_USERNAME": dependencies["EVIA_TEST_CLICKHOUSE_USERNAME"],
        "EVIA_CLICKHOUSE_PASSWORD": dependencies["EVIA_TEST_CLICKHOUSE_PASSWORD"],
        "EVIA_QUERY_WORKERS": "2",
        "EVIA_BOOTSTRAP_USERNAME": "bench-admin", "EVIA_BOOTSTRAP_DISPLAY_NAME": "Synthetic benchmark administrator",
        "EVIA_BOOTSTRAP_PASSWORD": password, "EVIA_E2E_USERNAME": "bench-admin", "EVIA_E2E_PASSWORD": password,
    }
    write_private(runtime_path, settings)
    print("Private benchmark settings created; collection defaults remain enabled.")


def compose(dependency_path, arguments, payload=None):
    environment = {key: value for key, value in os.environ.items()
                   if key not in ("DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH")}
    result = subprocess.run([
        "docker", "--host=unix:///var/run/docker.sock", "compose", "--project-name", "evia-benchmark",
        "--env-file", str(dependency_path), "-f", str(ROOT / "deploy/compose.dev.yaml"),
        "-f", str(ROOT / "deploy/compose.benchmark.yaml"), "--profile", "pipeline", "exec", "-T", *arguments,
    ], input=payload, env=environment, text=True, capture_output=True)
    if result.returncode:
        fail("Benchmark dependency preparation failed; dependency details omitted.")
    return result.stdout


def prepare(dependency_path, runtime_path):
    dependency_path, dependencies = load(dependency_path)
    _, runtime = load(runtime_path)
    database = runtime.get("EVIA_CLICKHOUSE_DATABASE", "")
    if not NAMESPACE.fullmatch(database):
        fail("Preparation requires a dedicated synthetic benchmark namespace.")
    for key, expected in (("EVIA_TEST_KAFKA_BROKERS", "127.0.0.1:19093"),
                          ("EVIA_TEST_CLICKHOUSE_URL", "http://127.0.0.1:18124")):
        if dependencies.get(key) != expected:
            fail("Only the local benchmark Compose profile is supported.")
    for key in ("KAFKA_BROKERS", "CLICKHOUSE_URL", "CLICKHOUSE_USERNAME", "CLICKHOUSE_PASSWORD", "REDIS_URL"):
        if runtime.get("EVIA_" + key) != dependencies.get("EVIA_TEST_" + key):
            fail("Runtime and dependency settings must describe the same services.")
    for key, suffix in (("EVIA_KAFKA_TOPIC", ".events"), ("EVIA_KAFKA_DEAD_LETTER_TOPIC", ".quarantine"),
                        ("EVIA_KAFKA_PROBE_TOPIC", ".readiness")):
        topic = database + suffix
        if runtime.get(key) != topic:
            fail("Topics must belong to the benchmark namespace.")
        compose(dependency_path, ["kafka", "/opt/kafka/bin/kafka-topics.sh", "--bootstrap-server", "localhost:9092",
                "--create", "--if-not-exists", "--topic", topic, "--partitions", "1", "--replication-factor", "1",
                "--config", "min.insync.replicas=1", "--config", "retention.ms=86400000",
                "--config", "retention.bytes=2147483648", "--config", "segment.bytes=67108864"])

    def sql(statement, selected="default"):
        return compose(dependency_path, ["clickhouse", "/bin/sh", "-ec",
            'exec clickhouse-client --host 127.0.0.1 --user "$CLICKHOUSE_USER" '
            '--password "$CLICKHOUSE_PASSWORD" --database "$1" --multiquery', "benchmark-runtime", selected], statement)

    sql(f"CREATE DATABASE IF NOT EXISTS {database};")
    sql("CREATE TABLE IF NOT EXISTS benchmark_migrations (name String, checksum FixedString(64)) "
        "ENGINE = MergeTree ORDER BY name;", database)
    applied = dict(line.split("\t") for line in sql(
        "SELECT name, checksum FROM benchmark_migrations FORMAT TabSeparated;", database).splitlines())
    for migration in sorted((ROOT / "deploy/clickhouse").glob("*.sql")):
        contents = migration.read_text(encoding="utf-8")
        checksum = hashlib.sha256(contents.encode()).hexdigest()
        if migration.name in applied:
            if applied[migration.name] != checksum:
                fail("Applied migration changed; use a fresh benchmark namespace.")
            continue
        if migration.name in ("001_event_facts.sql", "002_readiness.sql"):
            contents = contents.replace("CREATE TABLE ", "CREATE TABLE IF NOT EXISTS ")
            contents = contents.replace("CREATE VIEW ", "CREATE VIEW IF NOT EXISTS ")
        sql(contents, database)
        sql(f"INSERT INTO benchmark_migrations VALUES ('{migration.name}', '{checksum}');", database)
        print("Applied benchmark ClickHouse migration " + migration.name + ".")
    print("Benchmark topics and migration chain ready; no event facts seeded.")


def main():
    if len(sys.argv) < 4:
        fail("Usage: runtime.py init|prepare DEPS_ENV RUNTIME_ENV | run RUNTIME_ENV -- COMMAND ...")
    action = sys.argv[1]
    if action == "init" and len(sys.argv) == 4:
        initialize(*sys.argv[2:])
    elif action == "prepare" and len(sys.argv) == 4:
        prepare(*sys.argv[2:])
    elif action == "run":
        _, settings = load(sys.argv[2])
        command = sys.argv[3:]
        if command and command[0] == "--":
            command = command[1:]
        if not command:
            fail("run requires a command.")
        environment = dict(os.environ)
        environment.update(settings)
        raise SystemExit(subprocess.run(command, env=environment, cwd=ROOT).returncode)
    else:
        fail("Unknown benchmark runtime action or arguments.")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        fail("Benchmark runtime failed (" + type(error).__name__ + "); values omitted.")
