import math
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from zipf_distribution_drill import (
    DrillError,
    evaluate_zipf,
    hamilton_apportion,
    head_tail_ratio,
    log_log_slope,
    parse_zipf_summary,
    zipf_weights,
)


class ZipfWeightsTest(unittest.TestCase):
    def test_inverse_power_law_shape(self):
        weights = zipf_weights(4, 1.0)
        self.assertEqual(weights[0], 1.0)
        self.assertAlmostEqual(weights[1], 0.5)
        self.assertAlmostEqual(weights[3], 0.25)
        self.assertEqual(weights, sorted(weights, reverse=True))

    def test_higher_exponent_concentrates_more(self):
        mild = zipf_weights(100, 0.5)
        steep = zipf_weights(100, 2.0)
        mild_share = mild[0] / sum(mild)
        steep_share = steep[0] / sum(steep)
        self.assertGreater(steep_share, mild_share)

    def test_rejects_bad_inputs(self):
        with self.assertRaises(DrillError):
            zipf_weights(1, 1.0)
        for exponent in (0.0, -1.0, 4.0):
            with self.assertRaises(DrillError):
                zipf_weights(10, exponent)


class HamiltonApportionTest(unittest.TestCase):
    def test_seats_sum_exactly_to_total(self):
        weights = zipf_weights(500, 1.0)
        seats = hamilton_apportion(weights, 4000)
        self.assertEqual(sum(seats), 4000)
        self.assertEqual(len(seats), 500)
        self.assertTrue(all(seat >= 1 for seat in seats))

    def test_deterministic_and_rank_ordered(self):
        weights = zipf_weights(100, 1.0)
        first = hamilton_apportion(weights, 1000)
        second = hamilton_apportion(weights, 1000)
        self.assertEqual(first, second)
        self.assertEqual(first, sorted(first, reverse=True))

    def test_uniform_weights_share_evenly(self):
        seats = hamilton_apportion([1.0, 1.0, 1.0, 1.0], 10)
        self.assertEqual(sorted(seats), [2, 2, 3, 3])

    def test_rejects_undercovered_total(self):
        with self.assertRaises(DrillError):
            hamilton_apportion(zipf_weights(10, 1.0), 5)
        with self.assertRaises(DrillError):
            hamilton_apportion(zipf_weights(10, 1.0), 0)


class LogLogSlopeTest(unittest.TestCase):
    def test_exact_power_law_recovers_exponent(self):
        # counts exactly proportional to 1/r^1.5 give slope -1.5 with no noise.
        counts = [max(1, round(10000 / (rank ** 1.5))) for rank in range(1, 200)]
        slope = log_log_slope(counts)
        self.assertAlmostEqual(slope, -1.5, delta=0.05)

    def test_realistic_zipf_apportionment_near_minus_one(self):
        seats = hamilton_apportion(zipf_weights(500, 1.0), 4000)
        slope = log_log_slope(seats)
        self.assertLess(abs(slope - (-1.0)), 0.15)

    def test_rejects_non_positive_counts(self):
        with self.assertRaises(DrillError):
            log_log_slope([5, 0, 2])
        with self.assertRaises(DrillError):
            log_log_slope([1])


class HeadTailRatioTest(unittest.TestCase):
    def test_ratio(self):
        self.assertEqual(head_tail_ratio([10, 5, 1]), 10.0)

    def test_rejects_empty(self):
        with self.assertRaises(DrillError):
            head_tail_ratio([])


def query_block(samples=10, p95_ms=40, errors=0):
    return {"samples": samples, "p95_ms": p95_ms, "errors": errors}


def zipf_summary(**overrides):
    base = {
        "type": "zipf_distribution_summary",
        "total_events": 4000,
        "cardinality": 500,
        "exponent": 1.0,
        "covered_identities": 500,
        "observed_slope": -1.03,
        "exact_counts_match": True,
        "hottest_count": 589,
        "coldest_count": 1,
        "hot_query": query_block(p95_ms=35),
        "tail_query": query_block(p95_ms=30),
        "cardinality_query": query_block(p95_ms=42),
        "peak_query_memory": 20000000,
    }
    base.update(overrides)
    return base


class EvaluateZipfTest(unittest.TestCase):
    def test_happy_summary_passes(self):
        verdict = evaluate_zipf(zipf_summary(), slope_tolerance=0.15)
        self.assertTrue(verdict["exact_counts_match"])
        self.assertEqual(verdict["observed_slope"], -1.03)
        self.assertAlmostEqual(verdict["head_tail_ratio"], 589.0)

    def test_missing_field_fails_closed(self):
        broken = zipf_summary()
        del broken["observed_slope"]
        with self.assertRaises(DrillError):
            evaluate_zipf(broken, 0.15)

    def test_uncovered_identities_fail(self):
        with self.assertRaises(DrillError):
            evaluate_zipf(zipf_summary(covered_identities=499), 0.15)

    def test_count_mismatch_fails(self):
        with self.assertRaises(DrillError):
            evaluate_zipf(zipf_summary(exact_counts_match=False), 0.15)

    def test_slope_outside_tolerance_fails(self):
        with self.assertRaises(DrillError):
            evaluate_zipf(zipf_summary(observed_slope=-0.7), 0.15)

    def test_slope_inside_tolerance_passes(self):
        verdict = evaluate_zipf(zipf_summary(observed_slope=-1.14), 0.15)
        self.assertEqual(verdict["expected_slope"], -1.0)

    def test_query_error_and_latency_bound(self):
        with self.assertRaises(DrillError):
            evaluate_zipf(zipf_summary(tail_query=query_block(errors=1)), 0.15)
        with self.assertRaises(DrillError):
            evaluate_zipf(zipf_summary(hot_query=query_block(p95_ms=12000)), 0.15)

    def test_zero_peak_memory_fails(self):
        with self.assertRaises(DrillError):
            evaluate_zipf(zipf_summary(peak_query_memory=0), 0.15)


class ParseSummaryTest(unittest.TestCase):
    def test_finds_summary_ignoring_spacing(self):
        line = '{"exponent":1,"type":"zipf_distribution_summary","total_events":4000}'
        parsed = parse_zipf_summary("=== RUN\n" + line + "\nPASS\n")
        self.assertEqual(parsed["exponent"], 1)

    def test_missing_returns_none(self):
        self.assertIsNone(parse_zipf_summary("ok\tgithub.com/example\t1s"))


if __name__ == "__main__":
    unittest.main()
