import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from redis_failover_drill import DrillError, evaluate, parse_event_log


def event(result, phase=None, code=None):
    value = {"t": "2026-11-13T00:00:00Z", "result": result}
    if phase:
        value["phase"] = phase
    if code:
        value["code"] = code
    return value


def summary(ok, errors, tokens, capacity, key="evia:test:{p:e}", refill_per_ms=20):
    return {"type": "summary", "t": "2026-11-13T00:00:00Z", "ok": str(ok), "errors": str(errors),
            "tokens": str(tokens), "capacity": str(capacity), "refill_per_ms": str(refill_per_ms),
            "key": key}


class ParseEventLogTest(unittest.TestCase):
    def test_blank_lines_ignored_and_json_decoded(self):
        text = "\n".join(["", json.dumps(event("ok")), "", json.dumps(event("error"))])
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
        # 20 accepted, 7 outage errors, then same-process recovery success.
        events = [event("ok") for _ in range(19)]
        events += [event("error", code="COLLECTION_BUDGET_UNAVAILABLE") for _ in range(7)]
        events.append(event("ok", phase="recovery"))
        capacity = 1000 * 1_000_000
        tokens = capacity - 20 * 1_000_000 + 1200
        events.append(summary(20, 7, tokens, capacity))
        verdict = evaluate(events, burst=1000, duration_ms=45_000)
        self.assertEqual(verdict["accepted_reserves"], 20)
        self.assertEqual(verdict["failed_reserves"], 7)
        self.assertIn(20 * 1_000_000 - 45_000 * 20, verdict["exact_debit_band"])

    def test_no_observed_failure_fails(self):
        events = [event("ok") for _ in range(5)]
        events.append(summary(5, 0, 300 * 1_000_000 - 5 * 1_000_000, 300 * 1_000_000))
        with self.assertRaises(DrillError):
            evaluate(events, burst=300, duration_ms=45_000)

    def test_non_unavailable_errors_do_not_count_as_outage(self):
        events = [event("ok") for _ in range(5)]
        events += [event("error", code="COLLECTION_BUDGET_EXCEEDED") for _ in range(3)]
        events.append(event("ok", phase="recovery"))
        events.append(summary(6, 3, 1000 * 1_000_000 - 6 * 1_000_000, 1000 * 1_000_000))
        with self.assertRaises(DrillError):
            evaluate(events, burst=1000, duration_ms=45_000)

    def test_missing_recovery_fails(self):
        events = [event("ok") for _ in range(5)] + [event("error", code="COLLECTION_BUDGET_UNAVAILABLE") for _ in range(3)]
        events.append(summary(5, 3, 300 * 1_000_000 - 5 * 1_000_000, 300 * 1_000_000))
        with self.assertRaises(DrillError):
            evaluate(events, burst=300, duration_ms=45_000)

    def test_recovery_error_fails(self):
        events = [event("ok") for _ in range(5)] + [event("error", code="COLLECTION_BUDGET_UNAVAILABLE") for _ in range(3)]
        events.append(event("error", phase="recovery", code="COLLECTION_BUDGET_UNAVAILABLE"))
        events.append(summary(5, 4, 300 * 1_000_000 - 5 * 1_000_000, 300 * 1_000_000))
        with self.assertRaises(DrillError):
            evaluate(events, burst=300, duration_ms=45_000)

    def test_overspend_fails(self):
        events = [event("ok") for _ in range(301)] + [event("error", code="COLLECTION_BUDGET_UNAVAILABLE")]
        events.append(event("ok", phase="recovery"))
        events.append(summary(302, 1, 0, 1000 * 1_000_000))
        with self.assertRaises(DrillError):
            evaluate(events, burst=300, duration_ms=45_000)

    def test_double_debit_outside_band_fails(self):
        # One reserve debited twice would consume an extra full event cost.
        events = [event("ok") for _ in range(10)] + [event("error", code="COLLECTION_BUDGET_UNAVAILABLE") for _ in range(3)]
        events.append(event("ok", phase="recovery"))
        capacity = 300 * 1_000_000
        tokens = capacity - 12 * 1_000_000
        events.append(summary(11, 3, tokens, capacity))
        with self.assertRaises(DrillError):
            evaluate(events, burst=300, duration_ms=45_000)

    def test_summary_counter_mismatch_fails(self):
        events = [event("ok"), event("error"), event("ok", phase="recovery")]
        events.append(summary(99, 1, 0, 300 * 1_000_000))
        with self.assertRaises(DrillError):
            evaluate(events, burst=300, duration_ms=45_000)

    def test_missing_summary_fails(self):
        with self.assertRaises(DrillError):
            evaluate([event("ok"), event("error")], burst=300, duration_ms=45_000)

    def test_empty_stream_fails(self):
        with self.assertRaises(DrillError):
            evaluate([], burst=300, duration_ms=45_000)


if __name__ == "__main__":
    unittest.main()
