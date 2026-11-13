import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from clickhouse_keeper_quorum_drill import (
    DrillError,
    bounded_insert_sql,
    count_remains_frozen,
    evaluate_victim,
    has_majority,
    is_quorum_insert_failure,
    majority,
    parse_event_log,
    parse_non_negative_int,
    parse_replica_flag,
)


class MajorityTest(unittest.TestCase):
    def test_odd_and_even_electorates(self):
        self.assertEqual(majority(1), 1)
        self.assertEqual(majority(3), 2)
        self.assertEqual(majority(5), 3)
        self.assertEqual(majority(4), 3)

    def test_rejects_non_positive(self):
        for bad in (0, -1, "3"):
            with self.assertRaises(DrillError):
                majority(bad)

    def test_has_majority_boundaries(self):
        self.assertFalse(has_majority(1, 3))
        self.assertTrue(has_majority(2, 3))
        self.assertTrue(has_majority(3, 3))
        self.assertFalse(has_majority(2, 5))
        self.assertTrue(has_majority(3, 5))

    def test_rejects_impossible_counts(self):
        with self.assertRaises(DrillError):
            has_majority(4, 3)
        with self.assertRaises(DrillError):
            has_majority(-1, 3)


class BoundedInsertSQLTest(unittest.TestCase):
    def test_renders_settings_between_columns_and_values(self):
        sql = bounded_insert_sql("db.t", ["seq"], [100], 5)
        self.assertEqual(
            sql,
            "INSERT INTO db.t (seq) SETTINGS wait_for_async_insert_timeout=5 VALUES (100)",
        )

    def test_multi_value_rendering(self):
        sql = bounded_insert_sql("db.t", ["seq"], [1, 2, 3, 4], 30)
        self.assertTrue(sql.endswith("VALUES (1), (2), (3), (4)"))
        self.assertIn("wait_for_async_insert_timeout=30", sql)

    def test_rejects_timeout_outside_closed_range(self):
        for bad in (0, 601, "5"):
            with self.assertRaises(DrillError):
                bounded_insert_sql("db.t", ["seq"], [1], bad)

    def test_rejects_empty_parts(self):
        with self.assertRaises(DrillError):
            bounded_insert_sql("", ["seq"], [1], 5)
        with self.assertRaises(DrillError):
            bounded_insert_sql("db.t", [], [1], 5)
        with self.assertRaises(DrillError):
            bounded_insert_sql("db.t", ["seq"], [], 5)


class QuorumFailureClassificationTest(unittest.TestCase):
    STDERR = ("Received exception from server (version 26.8.36):\n"
              "Code: 159. DB::Exception: Wait for async insert timeout (5000 ms) "
              "exceeded: While executing WaitForAsyncInsert. (TIMEOUT_EXCEEDED)")

    def test_matches_real_server_error(self):
        self.assertTrue(is_quorum_insert_failure(159, self.STDERR))

    def test_rejects_other_failures(self):
        self.assertFalse(is_quorum_insert_failure(60, self.STDERR))
        self.assertFalse(is_quorum_insert_failure(159, "connection refused"))
        self.assertFalse(is_quorum_insert_failure(0, ""))


class ParseHelpersTest(unittest.TestCase):
    def test_non_negative_int(self):
        self.assertEqual(parse_non_negative_int(" 7 \n"), 7)
        self.assertEqual(parse_non_negative_int("0"), 0)
        for bad in ("", "x", "-2"):
            with self.assertRaises(DrillError):
                parse_non_negative_int(bad)

    def test_replica_flag_closed_set(self):
        self.assertEqual(parse_replica_flag("0"), 0)
        self.assertEqual(parse_replica_flag("1"), 1)
        for bad in ("2", "-1"):
            with self.assertRaises(DrillError):
                parse_replica_flag(bad)

    def test_frozen_readings(self):
        self.assertTrue(count_remains_frozen(["5", "5", "5"], 5))
        self.assertFalse(count_remains_frozen(["5", "6"], 5))
        with self.assertRaises(DrillError):
            count_remains_frozen([], 5)


def attempt(seq, result, phase=None, error=None):
    event = {"t": "2026-11-13T00:00:00Z", "seq": str(seq), "result": result}
    if phase:
        event["phase"] = phase
    if error:
        event["error"] = error
    return event


def summary(ok, errors, survivor, database="evia_ha_drilldb"):
    return {"type": "summary", "t": "2026-11-13T00:00:00Z",
            "ok": str(ok), "errors": str(errors),
            "survivor": str(survivor), "database": database}


def happy_stream():
    events = [attempt(i, "ok") for i in range(1, 5)]
    events += [attempt(5, "error", error="context deadline exceeded"),
               attempt(5, "error", error="context deadline exceeded")]
    events.append(attempt(5, "ok", phase="recovery"))
    events.append(summary(5, 2, 5))
    return events


class EventStreamTest(unittest.TestCase):
    def test_happy_stream_passes(self):
        verdict = evaluate_victim(happy_stream())
        self.assertEqual(verdict["committed_inserts"], 5)
        self.assertEqual(verdict["failed_attempts"], 2)
        self.assertEqual(verdict["survivor_count"], 5)

    def test_blank_lines_and_malformed_input(self):
        text = "\n".join(["", json.dumps(attempt(1, "ok")), ""])
        self.assertEqual(len(parse_event_log(text)), 1)
        with self.assertRaises(DrillError):
            parse_event_log("not-json")
        with self.assertRaises(DrillError):
            parse_event_log('{"unexpected": true}')

    def test_no_failure_fails_closed(self):
        events = [attempt(1, "ok"), attempt(2, "ok", phase="recovery"), summary(2, 0, 2)]
        with self.assertRaises(DrillError):
            evaluate_victim(events)

    def test_no_recovery_fails_closed(self):
        events = [attempt(1, "ok"), attempt(2, "error"), summary(1, 1, 1)]
        with self.assertRaises(DrillError):
            evaluate_victim(events)

    def test_survivor_mismatch_fails_closed(self):
        events = [attempt(1, "ok"), attempt(2, "error"),
                  attempt(2, "ok", phase="recovery"), summary(2, 1, 1)]
        with self.assertRaises(DrillError):
            evaluate_victim(events)

    def test_out_of_order_sequence_fails_closed(self):
        events = [attempt(1, "ok"), attempt(3, "ok", phase="recovery"),
                  attempt(2, "error"), summary(2, 1, 2)]
        with self.assertRaises(DrillError):
            evaluate_victim(events)

    def test_missing_database_fails_closed(self):
        events = happy_stream()
        events[-1] = summary(5, 2, 5, database="")
        with self.assertRaises(DrillError):
            evaluate_victim(events)


if __name__ == "__main__":
    unittest.main()
