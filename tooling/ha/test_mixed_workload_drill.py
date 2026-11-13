import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from mixed_workload_drill import (
    DrillError,
    evaluate_mixed,
    is_monotonic_non_decreasing,
    parse_mixed_summary,
    percentile,
    success_rate,
)


def summary(**overrides):
    base = {
        "produced": 600,
        "view_count": 600,
        "raw_count": 600,
        "read_samples": 991,
        "read_errors": 0,
        "p50_read_ms": 10,
        "p95_read_ms": 66,
        "peak_query_memory": 15025451,
        "monotonic_readers": 4,
        "reader_goroutines": 4,
        "drain_ms": 5270,
        "type": "mixed_workload_summary",
    }
    base.update(overrides)
    return base


class PercentileTest(unittest.TestCase):
    def test_nearest_rank_matches_go_convention(self):
        self.assertEqual(percentile([1, 2, 3, 4], 50), 3)  # index = 4*50/100 = 2
        self.assertEqual(percentile([1, 2, 3, 4], 95), 4)
        self.assertEqual(percentile([7], 95), 7)

    def test_rejects_bad_inputs(self):
        with self.assertRaises(DrillError):
            percentile([], 95)
        with self.assertRaises(DrillError):
            percentile([1, -2], 95)
        with self.assertRaises(DrillError):
            percentile([1], 101)


class MonotonicTest(unittest.TestCase):
    def test_non_decreasing_series(self):
        self.assertTrue(is_monotonic_non_decreasing([0, 1, 1, 5, 5, 9]))
        self.assertFalse(is_monotonic_non_decreasing([3, 2, 4]))

    def test_rejects_empty_and_non_integer(self):
        with self.assertRaises(DrillError):
            is_monotonic_non_decreasing([])
        with self.assertRaises(DrillError):
            is_monotonic_non_decreasing([1, "2"])


class SuccessRateTest(unittest.TestCase):
    def test_rate_math(self):
        self.assertEqual(success_rate(10, 0), 1.0)
        self.assertAlmostEqual(success_rate(8, 2), 0.8)

    def test_rejects_bad_inputs(self):
        with self.assertRaises(DrillError):
            success_rate(0, 0)
        with self.assertRaises(DrillError):
            success_rate(5, -1)


class ParseSummaryTest(unittest.TestCase):
    def test_finds_summary_ignoring_spacing(self):
        line = '{"p95_read_ms":66,"type":"mixed_workload_summary","produced":600}'
        parsed = parse_mixed_summary("=== RUN\n" + line + "\nPASS\n")
        self.assertEqual(parsed["p95_read_ms"], 66)

    def test_missing_returns_none(self):
        self.assertIsNone(parse_mixed_summary("ok\tgithub.com/example\t1s"))


class EvaluateMixedTest(unittest.TestCase):
    def test_happy_summary_passes(self):
        verdict = evaluate_mixed(summary(), 10000)
        self.assertTrue(verdict["exact_once"])
        self.assertEqual(verdict["read_success_rate"], 1.0)
        self.assertEqual(verdict["p95_read_ms"], 66)

    def test_missing_field_fails_closed(self):
        broken = summary()
        del broken["p95_read_ms"]
        with self.assertRaises(DrillError):
            evaluate_mixed(broken, 10000)

    def test_exact_once_mismatch_fails(self):
        with self.assertRaises(DrillError):
            evaluate_mixed(summary(view_count=599), 10000)
        with self.assertRaises(DrillError):
            evaluate_mixed(summary(raw_count=500), 10000)

    def test_any_read_error_fails(self):
        with self.assertRaises(DrillError):
            evaluate_mixed(summary(read_errors=3), 10000)

    def test_non_monotonic_reader_fails(self):
        with self.assertRaises(DrillError):
            evaluate_mixed(summary(monotonic_readers=3), 10000)

    def test_latency_bound_enforced(self):
        with self.assertRaises(DrillError):
            evaluate_mixed(summary(p95_read_ms=12000), 10000)

    def test_zero_peak_memory_fails(self):
        with self.assertRaises(DrillError):
            evaluate_mixed(summary(peak_query_memory=0), 10000)

    def test_raw_replay_may_exceed_view(self):
        verdict = evaluate_mixed(summary(raw_count=650), 10000)
        self.assertTrue(verdict["exact_once"])


if __name__ == "__main__":
    unittest.main()
