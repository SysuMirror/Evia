#!/usr/bin/env python3
"""ClickHouse Keeper majority-loss drill for Evia.

The script reuses the standalone three-vote Keeper topology from
deploy/compose.ha-clickhouse.yaml (two embedded Keepers plus one standalone
Keeper, one shard with two ReplicatedMergeTree replicas) and proves four
invariants on the live system:

  1. Minority loss is tolerated. With one of three votes down the remaining
     two keep a leader and ReplicatedMergeTree writes keep completing.
  2. Majority loss fails closed. With two votes down no leader exists, the
     replica flips to readonly, inserts block and end in TIMEOUT_EXCEEDED
     (never a false success), while SELECT keeps serving the last durable
     data and the visible row count stays frozen for the whole outage.
  3. The production sink behaves the same way: its bounded HTTP client
     returns errors while the quorum is gone and the same long-lived sink
     resumes inserts after it returns, with ReplicatedMergeTree block
     deduplication keeping retried batches idempotent.
  4. Restoring one vote re-establishes the quorum and writes resume before
     the stopped replica is back; once it returns, Keeper reattachment
     drains the queue and both replicas converge to identical row sets.

A timed-out async insert is ambiguous: the server may retain the block and
commit it after the quorum returns, so the drill never claims a timed-out
row is absent. It asserts only that no write is acknowledged and no row
becomes visible while the quorum is down, then reports every ambiguous
sequence in the final report.

Only the Python standard library is used. Container control goes through the
docker CLI; all state is torn down with ``compose down -v``.
"""

import argparse
import json
import os
import subprocess
import sys
import tempfile
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
BACKEND = ROOT / "backend"
COMPOSE_FILE = ROOT / "deploy" / "compose.ha-clickhouse.yaml"
PROJECT = "evia-ha-ch-quorum"
PROBE_DATABASE = "evia_keeper_drill"
PROBE_TABLE = "quorum_probe"
BOUNDED_INSERT_TIMEOUT_SECONDS = 5
TOTAL_VOTES = 3
GO_ENV = {
    "EVIA_TEST_CH_HA_LB_URL": "http://127.0.0.1:18131",
    "EVIA_TEST_CH_HA_NODE1_URL": "http://127.0.0.1:18132",
    "EVIA_TEST_CH_HA_NODE2_URL": "http://127.0.0.1:18133",
    "EVIA_TEST_CH_HA_USERNAME": "evia",
    "EVIA_TEST_CH_HA_PASSWORD": "evia-ha-drill",
}


class DrillError(RuntimeError):
    """A Keeper quorum invariant could not be proven."""


# ---------------------------------------------------------------------------
# Pure logic (unit-tested without docker)
# ---------------------------------------------------------------------------

def majority(total):
    """Return the minimum vote count that forms a majority."""
    if not isinstance(total, int) or total < 1:
        raise DrillError("voter count must be a positive integer")
    return total // 2 + 1


def has_majority(alive, total):
    if not isinstance(alive, int) or not isinstance(total, int):
        raise DrillError("vote counts must be integers")
    if alive < 0 or alive > total:
        raise DrillError("alive votes cannot exceed the total electorate")
    return alive >= majority(total)


def bounded_insert_sql(table, columns, values, timeout_seconds):
    """Render an INSERT whose async wait is bounded by a SETTINGS clause.

    ClickHouse requires the column list before SETTINGS and VALUES after it;
    getting the order wrong is a syntax error, so the shape is pinned here.
    """
    if not isinstance(timeout_seconds, int) or not 1 <= timeout_seconds <= 600:
        raise DrillError("insert timeout must be an integer between 1 and 600 seconds")
    if not table or not columns or not values:
        raise DrillError("table, columns and values are required")
    column_list = ", ".join(columns)
    value_list = ", ".join(f"({value})" for value in values)
    return (f"INSERT INTO {table} ({column_list}) "
            f"SETTINGS wait_for_async_insert_timeout={timeout_seconds} VALUES {value_list}")


def is_quorum_insert_failure(returncode, stderr):
    """Classify the bounded insert failure observed without a Keeper leader."""
    text = (stderr or "").upper()
    return returncode == 159 and "TIMEOUT_EXCEEDED" in text and \
        "WAIT FOR ASYNC INSERT TIMEOUT" in text


def parse_non_negative_int(text):
    try:
        value = int(text.strip())
    except (AttributeError, ValueError) as exc:
        raise DrillError(f"expected a non-negative integer, got {text!r}") from exc
    if value < 0:
        raise DrillError(f"expected a non-negative integer, got {value}")
    return value


def parse_replica_flag(text):
    value = parse_non_negative_int(text)
    if value not in (0, 1):
        raise DrillError(f"expected a 0/1 replica flag, got {value}")
    return value


def count_remains_frozen(readings, baseline):
    """Every outage-window read must equal the last durable baseline count."""
    if not readings:
        raise DrillError("at least one outage reading is required")
    return all(parse_non_negative_int(reading) == baseline for reading in readings)


def parse_event_log(text):
    events = []
    for line_number, line in enumerate(text.splitlines(), start=1):
        if not line.strip():
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError as exc:
            raise DrillError(f"victim log line {line_number} is not valid JSON: {exc}") from exc
        if not isinstance(event, dict) or "result" not in event and event.get("type") != "summary":
            raise DrillError(f"victim log line {line_number} has an unknown event shape")
        events.append(event)
    return events


def evaluate_victim(events):
    """Apply the fail-closed/recovery invariants to one victim JSONL stream."""
    summary = next((event for event in events if event.get("type") == "summary"), None)
    attempts = [event for event in events if event.get("type") != "summary"]
    if not attempts:
        raise DrillError("victim recorded no insert attempts")
    errors = sum(1 for event in attempts if event.get("result") == "error")
    if errors == 0:
        raise DrillError("no failure was observed while the Keeper quorum was down")
    if not any(event.get("phase") == "recovery" and event.get("result") == "ok" for event in attempts):
        raise DrillError("the same sink did not recover after the quorum returned")

    expected_seq, committed = 1, []
    for event in attempts:
        seq = int(event["seq"])
        if event["result"] == "ok":
            if seq != expected_seq:
                raise DrillError(f"successful sequence {seq} out of order, expected {expected_seq}")
            committed.append(seq)
            expected_seq += 1
        elif seq != expected_seq:
            raise DrillError(f"failed attempt changed sequence from {expected_seq} to {seq}")
    ok = len(committed)
    if ok == 0:
        raise DrillError("victim committed nothing before the outage")

    if summary is None:
        raise DrillError("victim log lacks the final convergence summary")
    for key in ("ok", "errors", "survivor"):
        if key not in summary:
            raise DrillError(f"victim summary missing field {key}")
    if int(summary["ok"]) != ok or int(summary["errors"]) != errors:
        raise DrillError(f"summary counters do not match the event stream: {summary}")
    if int(summary["survivor"]) != ok:
        raise DrillError(f"survivor count {summary['survivor']} does not match successes {ok}")
    if not summary.get("database"):
        raise DrillError("summary must record the drill database for the convergence cross-check")
    return {
        "committed_inserts": ok,
        "failed_attempts": errors,
        "survivor_count": int(summary["survivor"]),
        "database": summary["database"],
    }


# ---------------------------------------------------------------------------
# Container orchestration
# ---------------------------------------------------------------------------

def run(command, *, env=None, check=True):
    proc = subprocess.run(command, text=True, env=env,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if check and proc.returncode != 0:
        raise DrillError(
            f"command failed ({proc.returncode}): {' '.join(command)}\n"
            f"{proc.stderr.strip() or proc.stdout.strip()}"
        )
    return proc


def compose(*args, check=True):
    return run(["docker", "compose", "-f", str(COMPOSE_FILE), "-p", PROJECT, *args], check=check)


def ch_sql(service, sql, *, check=True):
    proc = compose("exec", "-T", service, "clickhouse-client", "-u", "evia",
                   "--password", "evia-ha-drill", "-q", sql, check=check)
    return proc.stdout.strip()


def ch_sql_retry(service, sql, deadline_seconds=120):
    deadline = time.monotonic() + deadline_seconds
    while time.monotonic() < deadline:
        proc = compose("exec", "-T", service, "clickhouse-client", "-u", "evia",
                       "--password", "evia-ha-drill", "-q", sql, check=False)
        if proc.returncode == 0:
            return proc.stdout.strip()
        time.sleep(2)
    raise DrillError(f"SQL never succeeded on {service}: {sql}")


def wait_health(deadline_seconds=180):
    deadline = time.monotonic() + deadline_seconds
    while time.monotonic() < deadline:
        proc = compose("ps", "--format", "{{.Service}}|{{.Status}}", check=False)
        lines = [line for line in proc.stdout.splitlines() if line]
        if len(lines) == 4 and all("healthy" in line or line.startswith("keeper-3|") for line in lines):
            return
        time.sleep(2)
    raise DrillError("topology did not become healthy")


def leader_count(service):
    return parse_non_negative_int(ch_sql(
        service, "SELECT count() FROM system.keeper_cluster WHERE is_leader=1"))


def wait_leader(service, expected, deadline_seconds=60):
    deadline = time.monotonic() + deadline_seconds
    while time.monotonic() < deadline:
        proc = compose("exec", "-T", service, "clickhouse-client", "-u", "evia",
                       "--password", "evia-ha-drill", "-q",
                       "SELECT count() FROM system.keeper_cluster WHERE is_leader=1", check=False)
        if proc.returncode == 0:
            try:
                if parse_non_negative_int(proc.stdout) == expected:
                    return
            except DrillError:
                pass
        time.sleep(2)
    raise DrillError(f"{service} never reached leader count {expected}")


def replica_state(service):
    rows = ch_sql_retry(
        service,
        "SELECT is_readonly, is_session_expired, queue_size FROM system.replicas "
        f"WHERE table='{PROBE_TABLE}'")
    parts = rows.split("\t")
    if len(parts) != 3:
        raise DrillError(f"unexpected replica state shape: {rows!r}")
    return {
        "is_readonly": parse_replica_flag(parts[0]),
        "is_session_expired": parse_replica_flag(parts[1]),
        "queue_size": parse_non_negative_int(parts[2]),
    }


def wait_replica_readonly(service, readonly, deadline_seconds=90):
    deadline = time.monotonic() + deadline_seconds
    while time.monotonic() < deadline:
        try:
            if replica_state(service)["is_readonly"] == readonly:
                return
        except DrillError:
            pass
        time.sleep(2)
    raise DrillError(f"{service} replica never reached is_readonly={readonly}")


def wait_replica_drained(deadline_seconds=120):
    deadline = time.monotonic() + deadline_seconds
    while time.monotonic() < deadline:
        states = {}
        for service in ("ch-1", "ch-2"):
            proc = compose("exec", "-T", service, "clickhouse-client", "-u", "evia",
                           "--password", "evia-ha-drill", "-q",
                           "SELECT is_readonly, queue_size FROM system.replicas "
                           f"WHERE table='{PROBE_TABLE}'", check=False)
            if proc.returncode != 0:
                states = {}
                break
            readonly, queue = proc.stdout.strip().split("\t")
            states[service] = (parse_replica_flag(readonly), parse_non_negative_int(queue))
        if states and all(readonly == 0 and queue == 0 for readonly, queue in states.values()):
            return states
        time.sleep(2)
    raise DrillError("replicas did not reattach and drain their queues")


def table_count(service):
    return parse_non_negative_int(
        ch_sql_retry(service, f"SELECT count() FROM {PROBE_DATABASE}.{PROBE_TABLE}"))


def table_sequences(service):
    return ch_sql_retry(
        service,
        f"SELECT arraySort(groupArray(seq)) FROM {PROBE_DATABASE}.{PROBE_TABLE}")


def bounded_insert(service, seq):
    sql = bounded_insert_sql(
        f"{PROBE_DATABASE}.{PROBE_TABLE}", ["seq"], [str(seq)],
        BOUNDED_INSERT_TIMEOUT_SECONDS)
    started = time.monotonic()
    proc = compose("exec", "-T", service, "clickhouse-client", "-u", "evia",
                   "--password", "evia-ha-drill", "-q", sql, check=False)
    return {
        "seq": seq,
        "returncode": proc.returncode,
        "stderr": proc.stderr.strip(),
        "elapsed_ms": int((time.monotonic() - started) * 1000),
    }


def wait_successful_insert(service, first_seq, deadline_seconds=45):
    """Retry bounded inserts until the post-election cluster accepts writes.

    A fresh leader flag can appear before the replica's Keeper session has
    replayed its watches, so the first post-election write may still time
    out. The invariant is bounded recovery with a surviving majority, not
    instant acceptance; every failed stabilization attempt is recorded.
    """
    deadline = time.monotonic() + deadline_seconds
    attempts = []
    seq = first_seq
    while time.monotonic() < deadline:
        probe = bounded_insert(service, seq)
        attempts.append(probe)
        if probe["returncode"] == 0:
            return {"succeeded": probe, "stabilization_attempts": attempts}
        seq += 1
        time.sleep(2)
    raise DrillError(f"writes never resumed with a surviving majority: {attempts}")


def create_probe_table():
    for service in ("ch-1", "ch-2"):
        ch_sql(service, f"CREATE DATABASE IF NOT EXISTS {PROBE_DATABASE}")
    ddl = (
        f"CREATE TABLE IF NOT EXISTS {PROBE_DATABASE}.{PROBE_TABLE} "
        "(seq UInt64, written_at DateTime64(3,'UTC') DEFAULT now64(3)) "
        "ENGINE=ReplicatedMergeTree("
        f"'/clickhouse/tables/{{shard}}/{PROBE_DATABASE}/{PROBE_TABLE}','{{replica}}') ORDER BY seq"
    )
    ch_sql("ch-1", ddl)
    ch_sql_retry("ch-2", ddl)


def run_victim(duration_seconds, log_path, env):
    victim_env = dict(env)
    victim_env["EVIA_TEST_CH_KEEPER_VICTIM"] = "1"
    victim_env["EVIA_TEST_CH_KEEPER_VICTIM_LOG"] = log_path
    victim_env["EVIA_TEST_CH_KEEPER_VICTIM_DURATION"] = f"{duration_seconds}s"
    return subprocess.Popen(
        ["go", "test", "-tags=integration", "-count=1", "-v",
         "-run", "TestKeeperQuorumSinkVictim", "./internal/pipeline"],
        cwd=str(BACKEND), env=victim_env, text=True,
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
    )


def wait_victim_errors(log_path, minimum, deadline_seconds=120):
    deadline = time.monotonic() + deadline_seconds
    while time.monotonic() < deadline:
        try:
            events = parse_event_log(Path(log_path).read_text())
        except (OSError, DrillError):
            events = []
        if sum(1 for event in events if event.get("result") == "error") >= minimum:
            return
        time.sleep(2)
    raise DrillError(f"victim did not record {minimum} failures during the quorum outage")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--duration", type=int, default=70, help="victim loop duration in seconds")
    parser.add_argument("--keep", action="store_true", help="leave the topology running for inspection")
    args = parser.parse_args(argv)

    phases = []
    started = False
    topology_removed = False
    try:
        compose("up", "-d")
        started = True
        wait_health()
        for service in ("ch-1", "ch-2"):
            wait_leader(service, 1)
        phases.append({"phase": "quorum_health", "status": "ok", "total_votes": TOTAL_VOTES,
                       "majority": majority(TOTAL_VOTES)})

        create_probe_table()
        ch_sql("ch-1", bounded_insert_sql(
            f"{PROBE_DATABASE}.{PROBE_TABLE}", ["seq"], ["1", "2", "3", "4"], 30))
        baseline = 4
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            if table_count("ch-1") == baseline and table_count("ch-2") == baseline:
                break
            time.sleep(2)
        else:
            raise DrillError("baseline rows did not converge on both replicas")
        phases.append({"phase": "baseline_convergence", "status": "ok", "rows": baseline})

        # One vote down: two of three remain, the cluster must keep a leader.
        compose("stop", "keeper-3")
        wait_leader("ch-1", 1)
        wait_replica_readonly("ch-1", 0)
        if not has_majority(2, TOTAL_VOTES):
            raise DrillError("two of three votes must still form a majority")
        minority = wait_successful_insert("ch-1", 10)
        phases.append({"phase": "minority_loss_tolerated", "status": "ok",
                       "alive_votes": 2, **minority})
        compose("start", "keeper-3")
        wait_leader("ch-1", 1)
        wait_leader("ch-2", 1)
        # Stabilization retries can leave ambiguous blocks that commit after
        # the client timeout, so the outage baseline is the observed durable
        # count, never a hard-coded derivation.
        settle_deadline = time.monotonic() + 60
        while time.monotonic() < settle_deadline:
            outage_baseline = table_count("ch-1")
            if table_count("ch-2") == outage_baseline:
                break
            time.sleep(2)
        else:
            raise DrillError("replicas did not converge before the majority-loss phase")
        phases.append({"phase": "pre_outage_baseline", "status": "ok", "rows": outage_baseline})

        env = dict(os.environ)
        env.update(GO_ENV)
        with tempfile.TemporaryDirectory(prefix="evia-ch-keeper-") as tmp:
            log_path = os.path.join(tmp, "victim.jsonl")
            victim = run_victim(args.duration, log_path, env)
            time.sleep(5)

            # Two votes down: the single embedded Keeper left has no majority.
            outage_started = time.monotonic()
            compose("stop", "ch-2", "keeper-3")
            wait_leader("ch-1", 0)
            wait_replica_readonly("ch-1", 1)
            phases.append({"phase": "majority_loss_detected", "status": "ok",
                           "alive_votes": 1, "replica_state": replica_state("ch-1")})

            outage_readings = []
            probe_results = []
            for seq in (100, 101, 102):
                probe = bounded_insert("ch-1", seq)
                probe_results.append(probe)
                if not is_quorum_insert_failure(probe["returncode"], probe["stderr"]):
                    raise DrillError(f"expected a bounded quorum failure, got {probe}")
                outage_readings.append(ch_sql(
                    "ch-1", f"SELECT count() FROM {PROBE_DATABASE}.{PROBE_TABLE}"))
            # Reads stay available and keep serving the last durable data.
            select_rows = table_count("ch-1")
            if not count_remains_frozen(outage_readings, outage_baseline):
                raise DrillError(f"rows changed during the outage: {outage_readings}")
            if select_rows != outage_baseline:
                raise DrillError(
                    f"SELECT during outage returned {select_rows}, want {outage_baseline}")
            phases.append({"phase": "fail_closed_writes_reads_survive", "status": "ok",
                           "probes": probe_results, "frozen_counts": outage_readings,
                           "select_count": select_rows})

            wait_victim_errors(log_path, 2)

            # Restore one standalone vote: two of three again, quorum returns
            # while the ch-2 replica is still stopped.
            compose("start", "keeper-3")
            wait_leader("ch-1", 1)
            wait_replica_readonly("ch-1", 0)
            resumed = wait_successful_insert("ch-1", 200)
            phases.append({"phase": "quorum_restored_one_vote", "status": "ok",
                           "alive_votes": 2, **resumed})

            # Bring the stopped replica back and prove Keeper-led convergence.
            compose("start", "ch-2")
            drained = wait_replica_drained()
            victim_output = victim.communicate(timeout=args.duration + 90)[0]
            if victim.returncode != 0:
                raise DrillError(f"victim process failed:\n{victim_output}")
            verdict = evaluate_victim(parse_event_log(Path(log_path).read_text()))
            outage_ms = int((time.monotonic() - outage_started) * 1000)
            phases.append({"phase": "victim_evaluation", "status": "ok",
                           "outage_ms": outage_ms, "verdict": verdict,
                           "replica_states": drained})

            converge_deadline = time.monotonic() + 90
            sequences = {}
            while time.monotonic() < converge_deadline:
                sequences = {service: table_sequences(service) for service in ("ch-1", "ch-2")}
                counts = {service: table_count(service) for service in ("ch-1", "ch-2")}
                if len(set(sequences.values())) == 1 and len(set(counts.values())) == 1:
                    break
                time.sleep(2)
            if len(set(sequences.values())) != 1:
                raise DrillError(f"replicas diverged after recovery: {sequences}")
            final_sequences = json.loads(sequences["ch-1"])
            ambiguous = sorted(seq for seq in (100, 101, 102) if seq in final_sequences)
            phases.append({"phase": "two_node_convergence", "status": "ok",
                           "sequences": sequences, "ambiguous_probe_sequences": ambiguous})

            database = verdict["database"]
            for service in ("ch-1", "ch-2"):
                ch_sql(service, f"DROP DATABASE IF EXISTS {database} SYNC")
            for service in ("ch-1", "ch-2"):
                ch_sql_retry(service, f"DROP DATABASE IF EXISTS {PROBE_DATABASE} SYNC")

    finally:
        if started and not args.keep:
            compose("down", "-v")
            topology_removed = True

    report = {
        "status": "passed",
        "project": PROJECT,
        "duration_seconds": args.duration,
        "topology_removed": topology_removed,
        "phases": phases,
    }
    print(json.dumps(report, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except DrillError as exc:
        print(json.dumps({"status": "failed", "error": str(exc)}, indent=2), file=sys.stderr)
        sys.exit(1)
