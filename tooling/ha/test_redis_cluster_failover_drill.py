import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from redis_cluster_failover_drill import DrillError, evaluate, parse_event_log, parse_slot_ranges


def attempt(result, phase=None, code=None):
    event = {"t": "2026-11-13T00:00:00Z", "result": result}
    if phase:
        event["phase"] = phase
    if code:
        event["code"] = code
    return event


def summary(ok, errors, tokens, capacity, key="evia:test:{p:e}"):
    return {"type": "summary", "t": "2026-11-13T00:00:00Z",
            "ok": str(ok), "errors": str(errors),
            "tokens": str(tokens), "capacity": str(capacity), "key": key}


def happy_stream():
    # 300 successes at cost 1e6 each, 3 failures, one recovery success;
    # refill allowance 900m keeps the consumed value inside the band.
    events = [attempt("ok") for _ in range(300)]
    events += [attempt("error", code="COLLECTION_BUDGET_UNAVAILABLE") for _ in range(3)]
    events.append(attempt("ok", phase="recovery"))
    # capacity 1e9; 301 debits => 699e6 tokens before refill credit.
    events.append(summary(301, 3, 699_000_000, 1_000_000_000))
    return events


class ParseEventLogTest(unittest.TestCase):
    def test_blank_lines_ignored_and_json_decoded(self):
        text = "\n".join(["", json.dumps(attempt("ok")), "", json.dumps(attempt("error"))])
        events = parse_event_log(text)
        self.assertEqual([e["result"] for e in events], ["ok", "error"])

    def test_malformed_line_fails_closed(self):
        with self.assertRaises(DrillError):
            parse_event_log('{"result": "ok"\nnot-json')

    def test_unknown_shape_rejected(self):
        with self.assertRaises(DrillError):
            parse_event_log('{"unexpected": true}')


class EvaluateTest(unittest.TestCase):
    def test_happy_path_passes(self):
        verdict = evaluate(happy_stream(), duration_ms=45_000)
        self.assertEqual(verdict["committed_reserves"], 301)
        self.assertEqual(verdict["failed_attempts"], 3)
        self.assertEqual(verdict["consumed_token_units"], 301_000_000)

    def test_no_failure_fails(self):
        events = [attempt("ok"), attempt("ok"), attempt("ok", phase="recovery"),
                  summary(3, 0, 997_000_000, 1_000_000_000)]
        with self.assertRaises(DrillError):
            evaluate(events, 45_000)

    def test_missing_recovery_fails(self):
        events = [attempt("ok"), attempt("error"), attempt("ok"),
                  summary(2, 1, 998_000_000, 1_000_000_000)]
        with self.assertRaises(DrillError):
            evaluate(events, 45_000)

    def test_recovery_error_fails(self):
        events = [attempt("ok"), attempt("error"), attempt("error", phase="recovery"),
                  summary(1, 2, 999_000_000, 1_000_000_000)]
        with self.assertRaises(DrillError):
            evaluate(events, 45_000)

    def test_double_spend_outside_band_fails(self):
        # One extra unaccounted debit would push consumed above ok * cost.
        events = [attempt("ok"), attempt("error"), attempt("ok", phase="recovery"),
                  summary(2, 1, 996_000_000, 1_000_000_000)]
        with self.assertRaises(DrillError):
            evaluate(events, 45_000)

    def test_summary_counter_mismatch_fails(self):
        events = happy_stream()
        events[-1] = summary(99, 3, 699_000_000, 1_000_000_000)
        with self.assertRaises(DrillError):
            evaluate(events, 45_000)

    def test_missing_summary_fails(self):
        with self.assertRaises(DrillError):
            evaluate([attempt("ok"), attempt("error")], 45_000)

    def test_empty_stream_fails(self):
        with self.assertRaises(DrillError):
            evaluate([], 45_000)


class SlotRangeParseTest(unittest.TestCase):
    def test_parses_plain_and_migrating_ranges(self):
        master = ("id 172.31.80.10:6379@16379 myself,master - 0 0 1 connected 0-5460")
        self.assertEqual(parse_slot_ranges(master), [(0, 5460)])
        migrating = ("id 172.31.80.11:6379@16379 master - 0 0 2 connected 5461-10922 "
                     "10923->-85b1f3 [10923->-85b1f3]")
        # Migration markers (slot->node-id) are not owned slot ranges.
        self.assertEqual(parse_slot_ranges(migrating), [(5461, 10922)])

    def test_replica_line_has_no_ranges(self):
        replica = ("id 172.31.80.13:6379@16379 slave masterid 0 0 3 connected")
        self.assertEqual(parse_slot_ranges(replica), [])


if __name__ == "__main__":
    unittest.main()
