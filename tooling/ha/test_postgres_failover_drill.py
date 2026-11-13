import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from postgres_failover_drill import DrillError, evaluate, parse_event_log


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
    # seq 1..4 succeed, two attempts fail against seq 5 during the outage,
    # the recovery insert commits seq 5.
    events = [reserve(i, "ok") for i in range(1, 5)]
    events += [reserve(5, "error", error="connection refused"),
               reserve(5, "error", error="read-only")]
    events.append(reserve(5, "ok", phase="recovery"))
    events.append(summary(5, 2, 5, 5, 5))
    return events


class ParseEventLogTest(unittest.TestCase):
    def test_blank_lines_ignored_and_json_decoded(self):
        text = "\n".join(["", json.dumps(reserve(1, "ok")), "", json.dumps(reserve(1, "error"))])
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
        self.assertEqual(verdict["committed_inserts"], 5)
        self.assertEqual(verdict["failed_attempts"], 2)
        self.assertEqual(verdict["max_sequence"], 5)
        self.assertEqual(verdict["table_gaps"], 0)

    def test_no_failure_fails(self):
        events = [reserve(1, "ok"), reserve(2, "ok"), reserve(3, "ok", phase="recovery")]
        events.append(summary(3, 0, 3, 3, 3))
        with self.assertRaises(DrillError):
            evaluate(events)

    def test_missing_recovery_fails(self):
        events = [reserve(1, "ok"), reserve(2, "error"), reserve(2, "ok")]
        events.append(summary(2, 1, 2, 2, 2))
        with self.assertRaises(DrillError):
            evaluate(events)

    def test_recovery_error_fails(self):
        events = [reserve(1, "ok"), reserve(2, "error"),
                  reserve(2, "error", phase="recovery", error="still down")]
        events.append(summary(1, 2, 1, 1, 1))
        with self.assertRaises(DrillError):
            evaluate(events)

    def test_successful_sequence_gap_fails(self):
        # seq 2 reported success after seq 1, then seq 4 skips seq 3.
        events = [reserve(1, "ok"), reserve(2, "ok"), reserve(4, "ok"),
                  reserve(5, "error"), reserve(5, "ok", phase="recovery")]
        events.append(summary(4, 1, 4, 4, 4))
        with self.assertRaises(DrillError):
            evaluate(events)

    def test_failed_attempt_must_keep_sequence(self):
        events = [reserve(1, "ok"), reserve(3, "error"), reserve(3, "ok"),
                  reserve(4, "ok", phase="recovery")]
        events.append(summary(3, 1, 3, 3, 3))
        with self.assertRaises(DrillError):
            evaluate(events)

    def test_summary_counter_mismatch_fails(self):
        events = happy_stream()
        events[-1] = summary(99, 2, 5, 5, 5)
        with self.assertRaises(DrillError):
            evaluate(events)

    def test_summary_gap_flag_fails(self):
        events = happy_stream()
        events[-1] = summary(5, 2, 5, 5, 5, gaps=1)
        with self.assertRaises(DrillError):
            evaluate(events)

    def test_missing_summary_fails(self):
        with self.assertRaises(DrillError):
            evaluate([reserve(1, "ok"), reserve(2, "error")])

    def test_empty_stream_fails(self):
        with self.assertRaises(DrillError):
            evaluate([])


if __name__ == "__main__":
    unittest.main()
