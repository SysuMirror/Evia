import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from backlog_recovery_drill import (
    DrillError,
    drain_rate,
    evaluate_backlog,
    parse_backlog_summary,
    summarize_lag_samples,
)


def summary(**overrides):
    base = {
        "produced": 200,
        "baseline": 20,
        "backlog": 180,
        "peak_lag": 180,
        "final_lag": 0,
        "lag_samples": [180] * 16,
        "outage_ms": 9279,
        "drain_ms": 323,
        "drain_rate_per_s": 557.276,
        "view_count": 200,
        "raw_count": 200,
        "sink_attempts": 69,
        "type": "backlog_recovery_summary",
    }
    base.update(overrides)
    return base


class DrainRateTest(unittest.TestCase):
    def test_rate_math(self):
        self.assertAlmostEqual(drain_rate(180, 1000), 180.0)
        self.assertAlmostEqual(drain_rate(90, 500), 180.0)

    def test_rejects_bad_inputs(self):
        for bad_events in (0, -1, "180"):
            with self.assertRaises(DrillError):
                drain_rate(bad_events, 1000)
        for bad_ms in (0, -5, "323"):
            with self.assertRaises(DrillError):
                drain_rate(180, bad_ms)


class LagSamplesTest(unittest.TestCase):
    def test_frozen_series(self):
        result = summarize_lag_samples([180, 180, 180])
        self.assertTrue(result["frozen"])
        self.assertEqual(result["peak"], 180)
        self.assertEqual(result["samples"], 3)

    def test_non_frozen_series_detected(self):
        result = summarize_lag_samples([10, 20, 20])
        self.assertFalse(result["frozen"])
        self.assertEqual(result["peak"], 20)
        self.assertEqual(result["minimum"], 10)

    def test_rejects_empty_and_negative(self):
        with self.assertRaises(DrillError):
            summarize_lag_samples([])
        with self.assertRaises(DrillError):
            summarize_lag_samples([1, -2])


class ParseSummaryTest(unittest.TestCase):
    def test_finds_summary_ignoring_spacing_and_logs(self):
        line = '{"backlog":180,"type":"backlog_recovery_summary","final_lag":0}'
        parsed = parse_backlog_summary("=== RUN\n" + line + "\nPASS\n")
        self.assertEqual(parsed["backlog"], 180)
        self.assertEqual(parsed["final_lag"], 0)

    def test_missing_returns_none(self):
        self.assertIsNone(parse_backlog_summary("ok\tgithub.com/example\t1s"))


class EvaluateBacklogTest(unittest.TestCase):
    def test_happy_summary_passes(self):
        verdict = evaluate_backlog(summary())
        self.assertTrue(verdict["exact_once"])
        self.assertTrue(verdict["frozen_during_outage"])
        self.assertEqual(verdict["produced"], 200)
        self.assertEqual(verdict["peak_lag"], 180)
        self.assertAlmostEqual(verdict["drain_rate_per_s"], 557.276, places=2)

    def test_missing_field_fails_closed(self):
        broken = summary()
        del broken["final_lag"]
        with self.assertRaises(DrillError):
            evaluate_backlog(broken)

    def test_produced_must_equal_baseline_plus_backlog(self):
        with self.assertRaises(DrillError):
            evaluate_backlog(summary(produced=199))

    def test_nonzero_final_lag_fails(self):
        with self.assertRaises(DrillError):
            evaluate_backlog(summary(final_lag=7))

    def test_non_frozen_samples_fail(self):
        with self.assertRaises(DrillError):
            evaluate_backlog(summary(lag_samples=[180, 100]))

    def test_peak_lag_below_threshold_fails(self):
        with self.assertRaises(DrillError):
            evaluate_backlog(summary(lag_samples=[10] * 16, peak_lag=10))

    def test_dedup_view_mismatch_fails(self):
        with self.assertRaises(DrillError):
            evaluate_backlog(summary(view_count=199))

    def test_raw_table_cannot_have_fewer_rows(self):
        with self.assertRaises(DrillError):
            evaluate_backlog(summary(raw_count=150))

    def test_zero_attempts_fails(self):
        with self.assertRaises(DrillError):
            evaluate_backlog(summary(sink_attempts=0))

    def test_rate_disagreement_fails(self):
        with self.assertRaises(DrillError):
            evaluate_backlog(summary(drain_rate_per_s=10.0))

    def test_raw_may_exceed_view_when_replays_occur(self):
        # Append-only raw rows can outnumber the dedup view after a replay;
        # only "fewer than produced" is a data-loss signal.
        verdict = evaluate_backlog(summary(raw_count=205))
        self.assertTrue(verdict["exact_once"])


if __name__ == "__main__":
    unittest.main()
