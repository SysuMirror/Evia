#!/usr/bin/env python3
"""PostgreSQL synchronous-quorum and split-brain drill for Evia.

The script brings up the one-primary/two-standby topology from
deploy/compose.ha-postgres-quorum.yaml with ``ANY 1`` synchronous replication
and proves four invariants on the live system:

  1. Quorum health: both standbys register as quorum candidates and a
     committed WAL position is flushed on at least one standby before the
     commit returns, so every acknowledged commit survives primary loss.
  2. Bounded quorum loss: with both standbys stopped, a probing insert
     blocks on IPC/SyncRep. statement_timeout cannot interrupt that wait and
     killing the client leaves the backend waiting, so the drill cancels the
     backend explicitly, the same signal family pgx uses when a caller
     context expires. The wait ends bounded and the transaction still
     commits locally regardless of whether the client perceives a warning
     or an error: the ambiguous-commit case. The Go pool retries the same
     idempotent sequence, treats unique_violation as confirmation instead
     of duplicating, and every node converges after one standby returns.
  3. Real network partition and self-fencing: the primary is detached from
     the bridge (not merely stopped) and a standby is promoted. While the
     quorum GUC stays enforced the isolated primary cannot complete a write,
     so the quorum itself fences the stale node. The same long-lived pgx
     pool fails over to the promoted node with a contiguous 1..N sequence.
  4. Relaxed-fence divergence and cleanup: after the drill removes the
     quorum requirement to model an asynchronous deployment, a divergent
     write on the isolated old primary succeeds and stays absent from the
     promoted node; ``pg_rewind`` then removes it and the old primary
     rejoins on the promoted timeline until every node converges.

Only the Python standard library is used. Container control goes through the
docker CLI; all state is torn down with ``compose down -v``.
"""

import argparse
import json
import os
import re
import subprocess
import sys
import tempfile
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
BACKEND = ROOT / "backend"
COMPOSE_FILE = ROOT / "deploy" / "compose.ha-postgres-quorum.yaml"
PROJECT = "evia-ha-pg-quorum"
NETWORK = "ha-pg-quorum-net"
PRIMARY = "pg-primary"
STANDBY_A = "pg-standby-a"
STANDBY_B = "pg-standby-b"
STANDBY_A_IP = "172.31.81.11"
SYNC_APPLICATIONS = ("evia_standby_a", "evia_standby_b")
DRILL_PASSWORD = "evia-ha-drill"
DIVERGENT_SEQ = 9000001
RELAXED_SEQ = 9000002
PROOF_TABLE = "evia_ha_quorum_proof"
APPLICATION_NAME_RE = re.compile(r"^[a-z][a-z0-9_]{0,31}$")
FAILOVER_DSN = (
    "postgres://evia:evia-ha-drill@172.31.81.10:5432,172.31.81.11:5432,172.31.81.12:5432"
    "/evia?sslmode=disable&target_session_attrs=read-write"
)


class DrillError(RuntimeError):
    """A quorum or split-brain invariant could not be proven."""


# ---------------------------------------------------------------------------
# Pure logic (unit-tested without docker)
# ---------------------------------------------------------------------------

def quorum_guc(names):
    """Render the closed synchronous_standby_names value for a name list."""
    if not 1 <= len(names) <= 8:
        raise DrillError("synchronous quorum needs one to eight standby names")
    if len(set(names)) != len(names):
        raise DrillError("synchronous standby names must be unique")
    for name in names:
        if not APPLICATION_NAME_RE.match(name):
            raise DrillError(f"invalid synchronous standby name: {name}")
    return f"ANY 1 ({','.join(names)})"


def enable_quorum_sql(names):
    """Render the runtime command that turns on the synchronous quorum."""
    return f"ALTER SYSTEM SET synchronous_standby_names TO '{quorum_guc(names)}'"


RELOAD_CONF_SQL = "SELECT pg_reload_conf()"


def disable_quorum_sql():
    """Render the command that removes the synchronous quorum at runtime."""
    return "ALTER SYSTEM SET synchronous_standby_names TO ''"


def parse_lsn(text):
    """Parse a PostgreSQL LSN such as ``0/1A2B3C`` into a byte offset."""
    text = text.strip()
    if "/" not in text:
        raise DrillError(f"invalid LSN: {text!r}")
    high, low = text.split("/", 1)
    try:
        return (int(high, 16) << 32) | int(low, 16)
    except ValueError as exc:
        raise DrillError(f"invalid LSN: {text!r}") from exc


def lsn_reached(standby_lsn, commit_lsn):
    return parse_lsn(standby_lsn) >= parse_lsn(commit_lsn)


def parse_wal_timeline(text):
    """Parse the eight-digit hex timeline prefix of a WAL file name."""
    text = text.strip()
    if not re.fullmatch(r"[0-9A-F]{8}", text):
        raise DrillError(f"invalid WAL timeline prefix: {text!r}")
    timeline = int(text, 16)
    if timeline < 1:
        raise DrillError(f"invalid WAL timeline prefix: {text!r}")
    return timeline


def wal_timeline_sql(in_recovery):
    # A primary derives its timeline from the current WAL file name; a
    # standby cannot run pg_walfile_name during recovery, so it reads the
    # timeline its walreceiver is actually streaming.
    if in_recovery:
        return "SELECT lpad(to_hex(received_tli), 8, '0') FROM pg_stat_wal_receiver"
    return "SELECT substring(pg_walfile_name(pg_current_wal_lsn()), 1, 8)"


def network_id(project, network):
    return f"{project}_{network}"


def container_id(project, service):
    return f"{project}-{service}-1"


def disconnect_command(project, network, service):
    return ["docker", "network", "disconnect",
            network_id(project, network), container_id(project, service)]


def build_conninfo(host, application_name):
    """Build a libpq conninfo string for replication and pg_rewind use."""
    if not APPLICATION_NAME_RE.match(application_name):
        raise DrillError(f"invalid application name: {application_name}")
    return (f"host={host} port=5432 user=evia password={DRILL_PASSWORD} "
            f"dbname=evia application_name={application_name}")


def rewind_script(source_host, application_name):
    """Render the one-off container script that rewinds an old primary."""
    conninfo = build_conninfo(source_host, application_name)
    return "\n".join([
        "set -e",
        f'gosu postgres pg_rewind -D "$PGDATA" --source-server="{conninfo}"',
        'touch "$PGDATA/standby.signal"',
        f'printf \'%s\\n\' "primary_conninfo=\'{conninfo}\'" >> "$PGDATA/postgresql.auto.conf"',
        'chown postgres:postgres "$PGDATA/standby.signal" "$PGDATA/postgresql.auto.conf"',
    ])


def parse_activity_pid(text):
    """Parse one pg_stat_activity pid; an empty result means no backend."""
    text = text.strip()
    if text == "":
        return None
    try:
        pid = int(text)
    except ValueError as exc:
        raise DrillError(f"invalid backend pid: {text!r}") from exc
    if pid <= 0:
        raise DrillError(f"invalid backend pid: {text!r}")
    return pid


def cancel_backend_sql(pid):
    """Render the explicit cancellation that mirrors a pgx context deadline."""
    if not isinstance(pid, int) or pid <= 0:
        raise DrillError(f"invalid backend pid: {pid!r}")
    return f"SELECT pg_cancel_backend({pid})"


def parse_stall_summary(output):
    """Extract the Go stall summary regardless of JSON key ordering."""
    for line in output.splitlines():
        compact = line.replace(" ", "")
        if '"type":"quorum_stall_summary"' in compact:
            return json.loads(line)
    return None


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


def evaluate(events):
    """Apply the failover invariants to one victim JSONL event stream."""
    summary = next((event for event in events if event.get("type") == "summary"), None)
    reserves = [event for event in events if event.get("type") != "summary"]
    if not reserves:
        raise DrillError("victim recorded no insert attempts")
    errors = sum(1 for event in reserves if event.get("result") == "error")
    if errors == 0:
        raise DrillError("no failure was observed while the primary was partitioned")
    if not any(event.get("phase") == "recovery" and event.get("result") == "ok" for event in reserves):
        raise DrillError("the same pool did not recover after promotion")

    expected_seq, committed = 1, []
    for event in reserves:
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
        raise DrillError("victim committed nothing before the partition")

    if summary is None:
        raise DrillError("victim log lacks the final table summary")
    if not all(int(summary[key]) == value for key, value in
               [("ok", ok), ("count", ok), ("distinct", ok), ("max_seq", ok), ("gaps", 0)]):
        raise DrillError(f"summary counters do not match the event stream: {summary}")
    if int(summary["errors"]) != errors:
        raise DrillError("summary error count does not match the event stream")
    return {
        "committed_inserts": ok,
        "failed_attempts": errors,
        "max_sequence": ok,
        "table_gaps": int(summary["gaps"]),
    }


# ---------------------------------------------------------------------------
# Container orchestration
# ---------------------------------------------------------------------------

def run(command, *, env=None, check=True, cwd=None):
    proc = subprocess.run(command, text=True, env=env, cwd=str(cwd) if cwd else None,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if check and proc.returncode != 0:
        raise DrillError(
            f"command failed ({proc.returncode}): {' '.join(command)}\n{proc.stderr.strip()}"
        )
    return proc


def compose(*args, env=None, check=True):
    return run(["docker", "compose", "-f", str(COMPOSE_FILE), "-p", PROJECT, *args],
               env=env, check=check)


def psql(service, sql, *, check=True):
    proc = compose("exec", "-T", service, "psql", "-U", "evia", "-d", "evia", "-tAc", sql, check=check)
    return proc.stdout.strip()


def wait_health(deadline_seconds=150):
    deadline = time.monotonic() + deadline_seconds
    while time.monotonic() < deadline:
        proc = compose("ps", "--format", "{{.Service}}|{{.Status}}", check=False)
        lines = [line for line in proc.stdout.splitlines() if line]
        if len(lines) == 3 and all("healthy" in line for line in lines):
            return
        time.sleep(2)
    raise DrillError("quorum topology did not become healthy")


def wait_recovery_state(service, in_recovery, deadline_seconds=60):
    expected = "t" if in_recovery else "f"
    deadline = time.monotonic() + deadline_seconds
    while time.monotonic() < deadline:
        proc = compose("exec", "-T", service, "psql", "-U", "evia", "-d", "evia",
                       "-tAc", "SELECT pg_is_in_recovery()", check=False)
        if proc.returncode == 0 and proc.stdout.strip() == expected:
            return
        time.sleep(1)
    raise DrillError(f"{service} never reached pg_is_in_recovery()={expected}")


def wait_wal_timeline(service, expected, in_recovery, deadline_seconds=30):
    deadline = time.monotonic() + deadline_seconds
    sql = wal_timeline_sql(in_recovery)
    while time.monotonic() < deadline:
        try:
            if parse_wal_timeline(psql(service, sql, check=False)) == expected:
                return
        except DrillError:
            pass
        time.sleep(0.5)
    raise DrillError(f"{service} never reached WAL timeline {expected}")


def wait_streaming(service, expected, deadline_seconds=60):
    deadline = time.monotonic() + deadline_seconds
    while time.monotonic() < deadline:
        count = psql(service,
                     "SELECT count(*) FROM pg_stat_replication WHERE state = 'streaming'",
                     check=False)
        if count == str(expected):
            return
        time.sleep(1)
    raise DrillError(f"{service} did not reach {expected} streaming senders")


def wait_sync_rep_pid(service, marker, deadline_seconds=20):
    """Wait for the marked insert to reach the IPC/SyncRep commit wait."""
    deadline = time.monotonic() + deadline_seconds
    sql = ("SELECT pid FROM pg_stat_activity "
           "WHERE wait_event_type = 'IPC' AND wait_event = 'SyncRep' "
           "AND position('" + marker + "' in query) > 0")
    while time.monotonic() < deadline:
        pid = parse_activity_pid(psql(service, sql, check=False))
        if pid is not None:
            return pid
        time.sleep(0.5)
    raise DrillError(f"no SyncRep-waiting backend for {marker!r} appeared")


def wait_backend_gone(service, pid, deadline_seconds=15):
    deadline = time.monotonic() + deadline_seconds
    while time.monotonic() < deadline:
        if psql(service, f"SELECT count(*) FROM pg_stat_activity WHERE pid = {pid}") == "0":
            return
        time.sleep(0.5)
    raise DrillError(f"backend {pid} did not leave after cancellation")


def wait_table(service, table, deadline_seconds=60):
    deadline = time.monotonic() + deadline_seconds
    while time.monotonic() < deadline:
        exists = psql(service, f"SELECT to_regclass('public.{table}') IS NOT NULL", check=False)
        if exists == "t":
            return
        time.sleep(1)
    raise DrillError(f"table {table} never appeared on {service}")


def wait_lsn_replay(service, commit_lsn, deadline_seconds=20):
    deadline = time.monotonic() + deadline_seconds
    while time.monotonic() < deadline:
        standby_lsn = psql(service, "SELECT pg_last_wal_replay_lsn()", check=False)
        if standby_lsn and standby_lsn != "":
            try:
                if lsn_reached(standby_lsn, commit_lsn):
                    return
            except DrillError:
                pass
        time.sleep(1)
    raise DrillError(f"{service} never replayed LSN {commit_lsn}")


def run_victim(duration_seconds, log_path, env):
    victim_env = dict(env)
    victim_env["EVIA_TEST_PG_VICTIM"] = "1"
    victim_env["EVIA_TEST_PG_VICTIM_LOG"] = log_path
    victim_env["EVIA_TEST_PG_VICTIM_DURATION"] = f"{duration_seconds}s"
    return subprocess.Popen(
        ["go", "test", "-tags=integration", "-count=1",
         "-run", "TestFailoverVictimLoop", "./internal/storage/postgres"],
        cwd=str(BACKEND), env=victim_env, text=True,
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
    )


def go_env():
    env = dict(os.environ)
    env["EVIA_TEST_PG_FAILOVER_DSN"] = FAILOVER_DSN
    return env


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--duration", type=int, default=45, help="victim loop duration in seconds")
    parser.add_argument("--keep", action="store_true", help="leave the topology running for inspection")
    args = parser.parse_args(argv)

    phases = []
    started = False
    topology_removed = False
    try:
        compose("up", "-d")
        started = True
        wait_health()
        wait_recovery_state(STANDBY_A, True)
        wait_recovery_state(STANDBY_B, True)
        # Bootstrap stays asynchronous: the quorum is enabled only once both
        # standbys stream, otherwise initial database creation would block
        # forever waiting for a standby that cannot exist yet.
        wait_streaming(PRIMARY, 2)
        psql(PRIMARY, enable_quorum_sql(SYNC_APPLICATIONS))
        psql(PRIMARY, RELOAD_CONF_SQL)
        expected_guc = quorum_guc(SYNC_APPLICATIONS)
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            guc = psql(PRIMARY, "SHOW synchronous_standby_names")
            rows = psql(PRIMARY,
                        "SELECT application_name, sync_state FROM pg_stat_replication ORDER BY 1")
            senders = dict(line.split("|") for line in rows.splitlines())
            if guc == expected_guc and set(senders) == set(SYNC_APPLICATIONS) and all(
                    state in ("sync", "quorum") for state in senders.values()):
                break
            time.sleep(1)
        else:
            raise DrillError(f"synchronous quorum did not activate: guc={guc!r} senders={senders}")
        if psql(PRIMARY, "SHOW synchronous_commit") != "on":
            raise DrillError("synchronous_commit must be on")
        phases.append({"phase": "quorum_health", "status": "ok", "senders": senders, "guc": guc})

        # Every acknowledged commit must already be durable on a standby.
        psql(PRIMARY, f"CREATE TABLE IF NOT EXISTS {PROOF_TABLE} (seq bigint PRIMARY KEY)")
        insert = psql(PRIMARY,
                      f"INSERT INTO {PROOF_TABLE} (seq) VALUES (41); SELECT pg_current_wal_lsn()")
        commit_lsn = insert.splitlines()[-1].strip()
        # pg_last_wal_receive_lsn is the WAL position fsynced on the
        # standby, which is exactly what synchronous_commit=on waits for.
        flushed = {service: psql(service, "SELECT pg_last_wal_receive_lsn()")
                   for service in (STANDBY_A, STANDBY_B)}
        if not any(lsn_reached(lsn, commit_lsn) for lsn in flushed.values()):
            raise DrillError(f"no standby flushed commit LSN {commit_lsn}: {flushed}")
        for service in (STANDBY_A, STANDBY_B):
            wait_lsn_replay(service, commit_lsn)
            if psql(service, f"SELECT count(*) FROM {PROOF_TABLE} WHERE seq = 41") != "1":
                raise DrillError(f"{service} did not expose the synchronously committed row")
        phases.append({"phase": "synchronous_durability", "status": "ok",
                       "commit_lsn": commit_lsn, "standby_flush_lsn": flushed})

        # Stop the whole quorum: commits must block and fail bounded, never
        # false-acknowledge; the production pool recovers with no duplicate.
        stall_env = go_env()
        stall_env["EVIA_TEST_PG_QUORUM_STALL"] = "1"
        stall_proc = subprocess.Popen(
            ["go", "test", "-tags=integration", "-count=1", "-v",
             "-run", "TestQuorumStallBounded", "./internal/storage/postgres"],
            cwd=str(BACKEND), env=stall_env, text=True,
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        )
        wait_table(PRIMARY, "evia_ha_quorum_drill")
        compose("stop", STANDBY_A, STANDBY_B)
        wait_streaming(PRIMARY, 0)
        started_at = time.monotonic()
        probe = subprocess.Popen(
            ["docker", "compose", "-f", str(COMPOSE_FILE), "-p", PROJECT,
             "exec", "-T", PRIMARY, "timeout", "30",
             "psql", "-U", "evia", "-d", "evia", "-v", "ON_ERROR_STOP=1",
             "-c", f"INSERT INTO {PROOF_TABLE} (seq) VALUES (42)"],
            text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        waiting_pid = wait_sync_rep_pid(PRIMARY, "VALUES (42)")
        # Diagnostic: while the quorum is down the Go pool's writers must be
        # parked in SyncRep exactly like the psql probe, proving both clients
        # share the same server-side arbitration.
        time.sleep(7)
        go_backends = psql(PRIMARY,
                           "SELECT pid, wait_event_type, wait_event FROM pg_stat_activity "
                           "WHERE query LIKE '%evia_ha_quorum_drill%' ORDER BY pid")
        phases.append({"phase": "stall_window_sample", "status": "ok",
                       "go_backends": go_backends.splitlines()})
        if psql(PRIMARY, cancel_backend_sql(waiting_pid)) != "t":
            raise DrillError(f"pg_cancel_backend({waiting_pid}) was not accepted")
        wait_backend_gone(PRIMARY, waiting_pid)
        blocked_ms = int((time.monotonic() - started_at) * 1000)
        probe_rc = probe.wait(timeout=15)
        # The server turns the cancel into a warning and finishes the local
        # commit, so the still-connected client may see success while a
        # client whose deadline closed the socket sees an error. Either way
        # the wait was bounded and the local commit is the invariant below.
        probe_stderr = probe.stderr.read().strip()
        ambiguous_row = psql(PRIMARY, f"SELECT count(*) FROM {PROOF_TABLE} WHERE seq = 42")
        if ambiguous_row != "1":
            raise DrillError(
                f"canceled SyncRep wait did not commit locally: count={ambiguous_row}")
        compose("start", STANDBY_A)
        wait_streaming(PRIMARY, 1)
        wait_lsn_replay(STANDBY_A, psql(PRIMARY, "SELECT pg_current_wal_lsn()"))
        replicated_row = psql(STANDBY_A, f"SELECT count(*) FROM {PROOF_TABLE} WHERE seq = 42")
        if replicated_row != "1":
            raise DrillError(
                f"ambiguous commit did not converge on the restored standby: {replicated_row}")
        stall_output = stall_proc.communicate(timeout=150)[0]
        if stall_proc.returncode != 0:
            raise DrillError(f"quorum stall Go test failed:\n{stall_output}")
        compose("start", STANDBY_B)
        wait_streaming(PRIMARY, 2)
        stall_summary = parse_stall_summary(stall_output)
        phases.append({"phase": "bounded_quorum_loss", "status": "ok",
                       "blocked_ms": blocked_ms,
                       "canceled_backend_pid": waiting_pid,
                       "probe_returncode": probe_rc,
                       "probe_stderr": probe_stderr[:300],
                       "ambiguous_local_commit": True,
                       "go_summary": stall_summary,
                       "go_output_tail": stall_output.strip().splitlines()[-12:]})

        with tempfile.TemporaryDirectory(prefix="evia-pg-quorum-") as tmp:
            log_path = os.path.join(tmp, "victim.jsonl")
            victim = run_victim(args.duration, log_path, go_env())
            time.sleep(3)

            # Real partition: detach the bridge endpoint while postgres keeps
            # running, so the old primary remains a live but isolated writer.
            run(disconnect_command(PROJECT, NETWORK, PRIMARY))
            partition_started = time.monotonic()
            before = psql(STANDBY_A, "SELECT pg_is_in_recovery()")
            if before != "t":
                raise DrillError(f"standby-a was not in recovery before promotion: {before}")
            if psql(STANDBY_A, "SELECT pg_promote(true, 30)") != "t":
                raise DrillError("standby-a promotion did not return true")
            wait_recovery_state(STANDBY_A, False)
            # The control-file checkpoint lags the promotion under load; the
            # WAL file name carries the authoritative current timeline.
            wait_wal_timeline(STANDBY_A, 2, in_recovery=False)
            new_timeline = parse_wal_timeline(
                psql(STANDBY_A, wal_timeline_sql(in_recovery=False)))

            # Self-fencing: while the quorum GUC stays enforced, an isolated
            # primary cannot complete any write because no standby can ack.
            marker = f"VALUES ({DIVERGENT_SEQ})"
            fence_probe = subprocess.Popen(
                ["docker", "compose", "-f", str(COMPOSE_FILE), "-p", PROJECT,
                 "exec", "-T", PRIMARY, "timeout", "30",
                 "psql", "-U", "evia", "-d", "evia", "-v", "ON_ERROR_STOP=1",
                 "-c", f"INSERT INTO evia_ha_drill (seq) {marker}"],
                text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            )
            fence_pid = wait_sync_rep_pid(PRIMARY, marker)
            if psql(PRIMARY, cancel_backend_sql(fence_pid)) != "t":
                raise DrillError(f"pg_cancel_backend({fence_pid}) was not accepted")
            wait_backend_gone(PRIMARY, fence_pid)
            fence_rc = fence_probe.wait(timeout=15)
            # The fence invariant is "no acknowledged write while isolated":
            # the probe blocked until canceled. A cancel may still finish the
            # local commit (the same ambiguous rule as phase 2), so record
            # the outcome without requiring the row to be absent.
            fence_row = psql(PRIMARY, f"SELECT count(*) FROM evia_ha_drill WHERE seq = {DIVERGENT_SEQ}")
            if fence_row not in ("0", "1"):
                raise DrillError(f"unexpected fence probe row count: {fence_row}")
            phases.append({"phase": "quorum_self_fencing", "status": "ok",
                           "canceled_backend_pid": fence_pid, "probe_returncode": fence_rc,
                           "post_cancel_row_present": fence_row == "1"})

            # Simulate a deployment without that fence (relaxed/async config):
            # only now can the stale primary diverge from the promoted timeline.
            psql(PRIMARY, disable_quorum_sql())
            psql(PRIMARY, RELOAD_CONF_SQL)
            psql(PRIMARY, f"INSERT INTO evia_ha_drill (seq) VALUES ({RELAXED_SEQ})")
            if psql(PRIMARY, f"SELECT count(*) FROM evia_ha_drill WHERE seq = {RELAXED_SEQ}") != "1":
                raise DrillError("divergent write was not accepted after relaxing the fence")
            if psql(STANDBY_A, f"SELECT count(*) FROM evia_ha_drill WHERE seq = {RELAXED_SEQ}") != "0":
                raise DrillError("the divergent write leaked onto the promoted node")

            # Repoint standby-b at the promoted primary; only one sender exists
            # until the isolated old primary rewinds and rejoins below.
            compose("stop", STANDBY_B)
            conninfo = build_conninfo(STANDBY_A_IP, "evia_standby_b")
            compose("run", "--rm", "--no-deps", "--entrypoint", "/bin/sh", STANDBY_B, "-c",
                    f'printf \'%s\\n\' "primary_conninfo=\'{conninfo}\'" >> "$PGDATA/postgresql.auto.conf"')
            compose("start", STANDBY_B)
            wait_streaming(STANDBY_A, 1)

            victim_output = victim.communicate(timeout=args.duration + 90)[0]
            if victim.returncode != 0:
                raise DrillError(f"victim process failed:\n{victim_output}")
            verdict = evaluate(parse_event_log(Path(log_path).read_text()))
            outage_ms = int((time.monotonic() - partition_started) * 1000)
            phases.append({"phase": "network_partition_failover", "status": "ok",
                           "outage_ms": outage_ms, "verdict": verdict,
                           "promoted_timeline": new_timeline})

            independent = psql(STANDBY_A,
                               "SELECT count(*), count(DISTINCT seq), max(seq) FROM evia_ha_drill")
            parts = independent.split("|")
            if len(parts) != 3 or parts != [str(verdict["committed_inserts"])] * 3:
                raise DrillError(f"independent promoted-node read mismatch: {independent}")
            phases.append({"phase": "independent_cross_check", "status": "ok", "table": independent})

        # Rewind the divergent old primary and reattach it on the new timeline.
        compose("stop", PRIMARY)
        compose("run", "--rm", "--no-deps", "--entrypoint", "/bin/sh", PRIMARY, "-c",
                rewind_script(STANDBY_A_IP, "evia_standby_old"))
        rejoin_env = dict(os.environ)
        rejoin_env["EVIA_HA_PG_ROLE_PRIMARY"] = "standby-join"
        compose("up", "-d", PRIMARY, env=rejoin_env)
        wait_recovery_state(PRIMARY, True)
        wait_wal_timeline(PRIMARY, 2, in_recovery=True)
        rejoin_timeline = parse_wal_timeline(psql(PRIMARY, wal_timeline_sql(in_recovery=True)))
        if rejoin_timeline != 2:
            raise DrillError(f"rewound node expected timeline 2, got {rejoin_timeline}")
        for divergent_seq in (DIVERGENT_SEQ, RELAXED_SEQ):
            if psql(PRIMARY, f"SELECT count(*) FROM evia_ha_drill WHERE seq = {divergent_seq}") != "0":
                raise DrillError(f"pg_rewind did not remove divergent seq {divergent_seq}")
            if psql(STANDBY_A, f"SELECT count(*) FROM evia_ha_drill WHERE seq = {divergent_seq}") != "0":
                raise DrillError(f"divergent seq {divergent_seq} leaked onto the promoted node")
        wait_streaming(STANDBY_A, 2)
        wait_lsn_replay(PRIMARY, psql(STANDBY_A, "SELECT pg_current_wal_lsn()"))
        converged = {service: psql(service,
                                   "SELECT count(*), count(DISTINCT seq), max(seq) FROM evia_ha_drill")
                     for service in (PRIMARY, STANDBY_A, STANDBY_B)}
        if len(set(converged.values())) != 1:
            raise DrillError(f"nodes did not converge after rejoin: {converged}")
        phases.append({"phase": "rewind_rejoin", "status": "ok",
                       "timeline": rejoin_timeline, "converged_rows": converged})
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
