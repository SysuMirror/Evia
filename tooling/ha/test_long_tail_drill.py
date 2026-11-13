import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from long_tail_drill import (
    DrillError,
    assign_uid_index,
    concentration_metrics,
    evaluate_long_tail,
    expected_counts,
    hot_event_count,
    parse_long_tail_summary,
)


class HotEventCountTest(unittest.TestCase):
    def test_rounding(self):
        self.assertEqual(hot_event_count(100, 0.8), 80)
        self.assertEqual(hot_event_count(10, 0.33), 3)

    def test_rejects_bad_inputs(self):
        with self.assertRaises(DrillError):
            hot_event_count(0, 0.8)
        for bad_share in (0, 1, -0.2, 1.5):
            with self.assertRaises(DrillError):
                hot_event_count(100, bad_share)


class AssignUidIndexTest(unittest.TestCase):
    def test_hot_block_rotates_within_hot_set(self):
        # 100 events, 10 identities, 3 hot, 80% -> 80 hot events.
        self.assertEqual(assign_uid_index(0, 100, 10, 3, 0.8), 0)
        self.assertEqual(assign_uid_index(1, 100, 10, 3, 0.8), 1)
        self.assertEqual(assign_uid_index(2, 100, 10, 3, 0.8), 2)
        self.assertEqual(assign_uid_index(3, 100, 10, 3, 0.8), 0)
        self.assertEqual(assign_uid_index(79, 100, 10, 3, 0.8), 79 % 3)

    def test_sparse_block_never_returns_hot_index(self):
        for ordinal in range(80, 100):
            index = assign_uid_index(ordinal, 100, 10, 3, 0.8)
            self.assertGreaterEqual(index, 3)
            self.assertLess(index, 10)

    def test_sparse_rotation_start(self):
        # First sparse event maps to the first sparse identity (index 3).
        self.assertEqual(assign_uid_index(80, 100, 10, 3, 0.8), 3)

    def test_rejects_bad_shape(self):
        with self.assertRaises(DrillError):
            assign_uid_index(0, 100, 10, 10, 0.8)  # hot_count == cardinality
        with self.assertRaises(DrillError):
            assign_uid_index(0, 100, 10, 0, 0.8)
        with self.assertRaises(DrillError):
            assign_uid_index(100, 100, 10, 3, 0.8)  # ordinal out of range


class ExpectedCountsTest(unittest.TestCase):
    def test_partition_matches_rule(self):
        counts = expected_counts(100, 10, 3, 0.8)
        self.assertEqual(len(counts), 10)
        self.assertEqual(sum(counts), 100)
        # 80 hot events across 3 identities; 20 sparse across 7.
        self.assertEqual(sum(counts[:3]), 80)
        self.assertEqual(sum(counts[3:]), 20)
        self.assertTrue(all(counts[i] > 0 for i in range(10)))

    def test_deterministic(self):
        first = expected_counts(500, 50, 5, 0.7)
        second = expected_counts(500, 50, 5, 0.7)
        self.assertEqual(first, second)

    def test_sparse_tail_partial_coverage_under_small_sample(self):
        # 1000 events, 800 identities, 20 hot at 80% leaves only 200 sparse
        # events rotating over 780 sparse identities: 200 distinct sparse
        # identities plus the 20 hot ones are covered, the deep tail is absent.
        counts = expected_counts(1000, 800, 20, 0.8)
        metrics = concentration_metrics(counts, 20)
        self.assertEqual(metrics["covered_identities"], 220)
        self.assertGreater(metrics["head_to_tail_ratio"], 1.0)

    def test_full_coverage_when_sample_exceeds_cardinality(self):
        # Enough sparse events (>= sparse identity count) cover every identity.
        counts = expected_counts(4000, 800, 20, 0.8)  # 800 sparse events over 780
        metrics = concentration_metrics(counts, 20)
        self.assertEqual(metrics["covered_identities"], 800)


class ConcentrationMetricsTest(unittest.TestCase):
    def test_observed_hot_share(self):
        counts = expected_counts(100, 10, 3, 0.8)
        metrics = concentration_metrics(counts, 3)
        self.assertAlmostEqual(metrics["hot_share_observed"], 0.8, places=6)
        self.assertEqual(metrics["total"], 100)
        self.assertEqual(metrics["cardinality"], 10)

    def test_rejects_empty_and_negative(self):
        with self.assertRaises(DrillError):
            concentration_metrics([], 3)
        with self.assertRaises(DrillError):
            concentration_metrics([1, -2], 1)
        with self.assertRaises(DrillError):
            concentration_metrics([0, 0], 1)


def query_block(samples=10, p95_ms=20, errors=0):
    return {"samples": samples, "p95_ms": p95_ms, "errors": errors}


def long_tail_summary(**overrides):
    base = {
        "type": "long_tail_summary",
        "total_events": 1000,
        "cardinality": 800,
        "hot_count": 20,
        "hot_share": 0.8,
        "covered_identities": 800,
        "hot_share_observed": 0.8,
        "hot_query": query_block(p95_ms=30),
        "tail_query": query_block(p95_ms=12),
        "cardinality_query": query_block(p95_ms=45),
        "exact_counts_match": True,
        "peak_query_memory": 12345678,
    }
    base.update(overrides)
    return base


class EvaluateLongTailTest(unittest.TestCase):
    def test_happy_summary_passes(self):
        verdict = evaluate_long_tail(long_tail_summary(), 10000)
        self.assertTrue(verdict["exact_counts_match"])
        self.assertEqual(verdict["cardinality"], 800)
        self.assertEqual(verdict["hot_p95_ms"], 30)

    def test_missing_field_fails_closed(self):
        broken = long_tail_summary()
        del broken["tail_query"]
        with self.assertRaises(DrillError):
            evaluate_long_tail(broken, 10000)

    def test_uncovered_identities_fail(self):
        with self.assertRaises(DrillError):
            evaluate_long_tail(long_tail_summary(covered_identities=799), 10000)

    def test_count_mismatch_fails(self):
        with self.assertRaises(DrillError):
            evaluate_long_tail(long_tail_summary(exact_counts_match=False), 10000)

    def test_hot_share_drift_fails(self):
        with self.assertRaises(DrillError):
            evaluate_long_tail(long_tail_summary(hot_share_observed=0.6), 10000)

    def test_query_error_fails(self):
        with self.assertRaises(DrillError):
            evaluate_long_tail(long_tail_summary(tail_query=query_block(errors=2)), 10000)

    def test_latency_bound_enforced_per_query(self):
        with self.assertRaises(DrillError):
            evaluate_long_tail(long_tail_summary(cardinality_query=query_block(p95_ms=12000)), 10000)

    def test_zero_samples_fail(self):
        with self.assertRaises(DrillError):
            evaluate_long_tail(long_tail_summary(hot_query=query_block(samples=0)), 10000)


class ParseSummaryTest(unittest.TestCase):
    def test_finds_summary_ignoring_spacing(self):
        line = '{"cardinality":800,"type":"long_tail_summary","total_events":1000}'
        parsed = parse_long_tail_summary("=== RUN\n" + line + "\nPASS\n")
        self.assertEqual(parsed["cardinality"], 800)

    def test_missing_returns_none(self):
        self.assertIsNone(parse_long_tail_summary("ok\tgithub.com/example\t1s"))


if __name__ == "__main__":
    unittest.main()
