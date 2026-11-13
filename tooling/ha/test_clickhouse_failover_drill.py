import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from clickhouse_failover_drill import DrillError, evaluate, parse_event_log


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
    events = [attempt(i, "ok") for i in range(1, 6)]
    events += [attempt(6, "error", error="connection refused"),
               attempt(6, "error", error="upstream unavailable")]
    events.append(attempt(6, "ok", phase="recovery"))
    events.append(summary(6, 2, 6))
    return events


class ParseEventLogTest(unittest.TestCase):
    def test_blank_lines_ignored_and_json_decoded(self):
        text = "\n".join(["", json.dumps(attempt(1, "ok")), "", json.dumps(attempt(1, "error"))])
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
        verdict = evaluate(happy_stream())
        self.assertEqual(verdict["committed_inserts"], 6)
        self.assertEqual(verdict["failed_attempts"], 2)
        self.assertEqual(verdict["survivor_count"], 6)
        self.assertEqual(verdict["database"], "evia_ha_drilldb")

    def test_no_failure_fails(self):
        events = [attempt(1, "ok"), attempt(2, "ok"), attempt(3, "ok", phase="recovery")]
        events.append(summary(3, 0, 3))
        with self.assertRaises(DrillError):
            evaluate(events)

    def test_missing_recovery_fails(self):
        events = [attempt(1, "ok"), attempt(2, "error"), attempt(2, "ok")]
        events.append(summary(2, 1, 2))
        with self.assertRaises(DrillError):
            evaluate(events)

    def test_recovery_error_fails(self):
        events = [attempt(1, "ok"), attempt(2, "error"),
                  attempt(2, "error", phase="recovery", error="still down")]
        events.append(summary(1, 2, 1))
        with self.assertRaises(DrillError):
            evaluate(events)

    def test_successful_sequence_gap_fails(self):
        events = [attempt(1, "ok"), attempt(2, "ok"), attempt(4, "ok"),
                  attempt(5, "error"), attempt(5, "ok", phase="recovery")]
        events.append(summary(4, 1, 4))
        with self.assertRaises(DrillError):
            evaluate(events)

    def test_failed_attempt_must_keep_sequence(self):
        events = [attempt(1, "ok"), attempt(3, "error"), attempt(3, "ok"),
                  attempt(4, "ok", phase="recovery")]
        events.append(summary(3, 1, 3))
        with self.assertRaises(DrillError):
            evaluate(events)

    def test_replica_divergence_fails(self):
        events = happy_stream()
        events[-1] = summary(6, 2, 5)
        with self.assertRaises(DrillError):
            evaluate(events)

    def test_summary_success_mismatch_fails(self):
        events = happy_stream()
        events[-1] = summary(99, 2, 6)
        with self.assertRaises(DrillError):
            evaluate(events)

    def test_missing_summary_fails(self):
        with self.assertRaises(DrillError):
            evaluate([attempt(1, "ok"), attempt(2, "error")])

    def test_empty_stream_fails(self):
        with self.assertRaises(DrillError):
            evaluate([])


if __name__ == "__main__":
    unittest.main()
