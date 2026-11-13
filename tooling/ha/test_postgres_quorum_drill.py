import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from postgres_quorum_drill import (
    RELOAD_CONF_SQL,
    DrillError,
    build_conninfo,
    cancel_backend_sql,
    disable_quorum_sql,
    enable_quorum_sql,
    container_id,
    disconnect_command,
    evaluate,
    lsn_reached,
    network_id,
    parse_activity_pid,
    parse_event_log,
    parse_lsn,
    parse_stall_summary,
    parse_wal_timeline,
    wal_timeline_sql,
    quorum_guc,
    rewind_script,
)


def reserve(seq, result, phase=None, error=None):
    event = {"t": "2026-11-13T00:00:00Z", "seq": str(seq), "result": result}
    if phase:
        event["phase"] = phase
    if error:
        event["error"] = error
    return event


def summary(ok, errors, count, distinct, max_seq, gaps=0):
    return {"type": "summary", "t": "2026-11-13T00:00:00Z",
            "ok": str(ok), "errors": str(errors), "count": str(count),
            "distinct": str(distinct), "max_seq": str(max_seq), "gaps": str(gaps)}


def happy_stream():
    events = [reserve(i, "ok") for i in range(1, 5)]
    events += [reserve(5, "error", error="no quorum"), reserve(5, "error", error="no quorum")]
    events.append(reserve(5, "ok", phase="recovery"))
    events.append(summary(5, 2, 5, 5, 5))
    return events


class QuorumGUCTest(unittest.TestCase):
    def test_renders_any_one_list(self):
        self.assertEqual(quorum_guc(["evia_standby_a", "evia_standby_b"]),
                         "ANY 1 (evia_standby_a,evia_standby_b)")

    def test_rejects_empty_duplicate_oversize_and_bad_names(self):
        with self.assertRaises(DrillError):
            quorum_guc([])
        with self.assertRaises(DrillError):
            quorum_guc(["same", "same"])
        with self.assertRaises(DrillError):
            quorum_guc([f"standby_{i}" for i in range(9)])
        for bad in ["1bad", "BAD", "has space", "bad-name"]:
            with self.assertRaises(DrillError):
                quorum_guc([bad])


class EnableQuorumSQLTest(unittest.TestCase):
    def test_statement_enables_runtime_quorum_without_restart(self):
        sql = enable_quorum_sql(["evia_standby_a", "evia_standby_b"])
        self.assertIn("ALTER SYSTEM SET synchronous_standby_names", sql)
        self.assertIn("'ANY 1 (evia_standby_a,evia_standby_b)'", sql)
        self.assertNotIn(";", sql)
        self.assertEqual(RELOAD_CONF_SQL, "SELECT pg_reload_conf()")


class DisableQuorumSQLTest(unittest.TestCase):
    def test_statement_clears_synchronous_standby_names(self):
        self.assertEqual(disable_quorum_sql(),
                         "ALTER SYSTEM SET synchronous_standby_names TO ''")


class LSNTest(unittest.TestCase):
    def test_parses_known_offsets(self):
        self.assertEqual(parse_lsn("0/0"), 0)
        self.assertEqual(parse_lsn("0/10"), 16)
        self.assertEqual(parse_lsn("1/0"), 1 << 32)
        self.assertEqual(parse_lsn("  1/A2 \n"), (1 << 32) | 0xA2)

    def test_rejects_malformed(self):
        for bad in ["", "nonsense", "0/ZZ"]:
            with self.assertRaises(DrillError):
                parse_lsn(bad)

    def test_reached_or_passed(self):
        self.assertTrue(lsn_reached("0/1A", "0/1A"))
        self.assertTrue(lsn_reached("0/1B", "0/1A"))
        self.assertFalse(lsn_reached("0/19", "0/1A"))


class WALTimelineTest(unittest.TestCase):
    def test_parses_hex_prefix(self):
        self.assertEqual(parse_wal_timeline("00000002\n"), 2)
        self.assertEqual(parse_wal_timeline("0000000A"), 10)

    def test_rejects_bad_prefix(self):
        for bad in ["", "x", "00000000", "0000002"]:
            with self.assertRaises(DrillError):
                parse_wal_timeline(bad)

    def test_recovery_uses_walreceiver_timeline(self):
        self.assertIn("received_tli", wal_timeline_sql(in_recovery=True))
        self.assertIn("pg_current_wal_lsn", wal_timeline_sql(in_recovery=False))


class DockerCommandTest(unittest.TestCase):
    def test_identifiers_and_disconnect_command(self):
        self.assertEqual(network_id("proj", "net"), "proj_net")
        self.assertEqual(container_id("proj", "pg-primary"), "proj-pg-primary-1")
        self.assertEqual(
            disconnect_command("proj", "net", "pg-primary"),
            ["docker", "network", "disconnect", "proj_net", "proj-pg-primary-1"],
        )


class ConninfoAndRewindTest(unittest.TestCase):
    def test_conninfo_fields(self):
        conninfo = build_conninfo("172.31.81.11", "evia_standby_b")
        self.assertIn("host=172.31.81.11", conninfo)
        self.assertIn("user=evia", conninfo)
        self.assertIn("password=evia-ha-drill", conninfo)
        self.assertIn("application_name=evia_standby_b", conninfo)

    def test_conninfo_rejects_bad_application_name(self):
        with self.assertRaises(DrillError):
            build_conninfo("172.31.81.11", "Bad Name")

    def test_rewind_script_prepares_standby_layout(self):
        script = rewind_script("172.31.81.11", "evia_standby_old")
        self.assertIn('gosu postgres pg_rewind -D "$PGDATA"', script)
        self.assertIn('host=172.31.81.11', script)
        self.assertIn('touch "$PGDATA/standby.signal"', script)
        self.assertIn("primary_conninfo=", script)
        self.assertIn("application_name=evia_standby_old", script)


class BackendCancelTest(unittest.TestCase):
    def test_parse_activity_pid(self):
        self.assertEqual(parse_activity_pid("401\n"), 401)
        self.assertIsNone(parse_activity_pid(""))
        for bad in ["0", "x"]:
            with self.assertRaises(DrillError):
                parse_activity_pid(bad)

    def test_cancel_backend_sql_rejects_bad_pid(self):
        self.assertEqual(cancel_backend_sql(401), "SELECT pg_cancel_backend(401)")
        for bad in (0, -3, "401"):
            with self.assertRaises(DrillError):
                cancel_backend_sql(bad)


class StallSummaryTest(unittest.TestCase):
    def test_finds_summary_ignoring_key_order_and_spaces(self):
        real_line = ('{"ambiguous_commits":1,"blocked_ms":6001,"committed_rows":18,'
                     '"stall_started_at":"2026-11-13T12:04:48Z",'
                     '"type":"quorum_stall_summary"}')
        summary = parse_stall_summary("=== RUN\n" + real_line + "\nPASS\n")
        self.assertEqual(summary["ambiguous_commits"], 1)
        self.assertEqual(summary["committed_rows"], 18)

    def test_missing_summary_returns_none(self):
        self.assertIsNone(parse_stall_summary("ok\tgithub.com/example\t1.0s"))


class EventStreamTest(unittest.TestCase):
    def test_happy_stream_passes(self):
        verdict = evaluate(happy_stream())
        self.assertEqual(verdict["committed_inserts"], 5)
        self.assertEqual(verdict["failed_attempts"], 2)
        self.assertEqual(verdict["table_gaps"], 0)

    def test_blank_lines_and_malformed_input(self):
        text = "\n".join(["", json.dumps(reserve(1, "ok")), ""])
        self.assertEqual(len(parse_event_log(text)), 1)
        with self.assertRaises(DrillError):
            parse_event_log("not-json")
        with self.assertRaises(DrillError):
            parse_event_log('{"unexpected": true}')

    def test_no_failure_fails_closed(self):
        events = [reserve(1, "ok"), reserve(2, "ok", phase="recovery"), summary(2, 0, 2, 2, 2)]
        with self.assertRaises(DrillError):
            evaluate(events)

    def test_out_of_order_sequence_fails_closed(self):
        events = [reserve(1, "ok"), reserve(3, "ok", phase="recovery"),
                  reserve(2, "error"), summary(2, 1, 2, 2, 2)]
        with self.assertRaises(DrillError):
            evaluate(events)


if __name__ == "__main__":
    unittest.main()
