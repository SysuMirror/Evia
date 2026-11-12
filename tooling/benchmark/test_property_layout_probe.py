"""Pure-logic tests for the property layout physical-read gate probe."""
import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import property_layout_probe as probe  # noqa: E402


class RowGenerationTest(unittest.TestCase):
    def test_deterministic_and_bounded(self):
        first = probe.generate_row(7)
        second = probe.generate_row(7)
        self.assertEqual(first, second)
        self.assertEqual(first["id"], 7)
        self.assertEqual(first["metric_value"], 7)
        other = probe.generate_row(107)
        self.assertEqual(other["metric_value"], 7)  # same modulo bucket
        self.assertNotEqual(first["properties_json"], other["properties_json"])

    def test_document_shape_matches_gate_profile(self):
        row = probe.generate_row(0)
        document = json.loads(row["properties_json"])
        self.assertEqual(len(document), probe.STRING_KEY_COUNT + 1)
        for slot in range(probe.STRING_KEY_COUNT):
            key = f"segment_{slot:02d}"
            self.assertIn(key, document)
        self.assertIn(probe.NUMERIC_KEY, document)
        self.assertEqual(document[probe.NUMERIC_KEY], 0)
        # Fixed-width string values keep the wide-document compression profile.
        self.assertEqual(len(document[probe.HOT_STRING_KEY]), probe.STRING_VALUE_WIDTH)

    def test_numeric_values_stay_inside_domain(self):
        for index in (0, 50, 99, 100, 12345):
            row = probe.generate_row(index)
            self.assertIn(row["metric_value"], range(probe.NUMERIC_DOMAIN))

    def test_render_is_single_json_line(self):
        line = probe.render_json_each_row(probe.generate_row(3))
        self.assertNotIn("\n", line)
        self.assertEqual(json.loads(line)["id"], 3)


class IdentifierSafetyTest(unittest.TestCase):
    def test_database_name_is_scratch_gated(self):
        for good in ("evia_probe_layout", "evia_probe_x12"):
            probe.validate_database(good)
        for bad in ("evia_browser_f87bd145e668", "default", "evia_probe_", "DROP TABLE x"):
            with self.assertRaises(probe.ProbeError):
                probe.validate_database(bad)

    def test_ddl_rejects_unsafe_identifiers(self):
        with self.assertRaises(probe.ProbeError):
            probe.legacy_ddl("probe; DROP TABLE x")
        with self.assertRaises(probe.ProbeError):
            probe.dedicated_ddl("probe_dedicated", "segment_03; DROP", "metric_07")

    def test_ddl_contains_only_expected_columns(self):
        ddl = probe.dedicated_ddl("probe_dedicated", "segment_03", "metric_07")
        self.assertIn("segment_03 String", ddl)
        self.assertIn("metric_07 Int64", ddl)
        self.assertTrue(ddl.startswith("CREATE TABLE probe_dedicated ("))


class QueryEquivalenceTest(unittest.TestCase):
    def test_numeric_filter_queries_cover_three_layouts(self):
        queries = probe.numeric_filter_queries("metric_07")
        self.assertEqual(set(queries), {"legacy", "map", "dedicated"})
        self.assertIn("JSONExtractInt(properties_json, 'metric_07')", queries["legacy"])
        self.assertIn("p_6D65747269635F3037", queries["map"])
        self.assertIn("metric_07 > 50", queries["dedicated"])

    def test_string_group_queries_cover_three_layouts(self):
        queries = probe.string_group_queries("segment_03")
        self.assertIn("JSONExtractString(properties_json, 'segment_03')", queries["legacy"])
        self.assertIn("p_7365676D656E745F3033", queries["map"])
        self.assertIn("GROUP BY segment_03", queries["dedicated"])

    def test_dedicated_query_requires_valid_column(self):
        with self.assertRaises(probe.ProbeError):
            probe.numeric_filter_queries("metric_07; DROP")


class VerdictTest(unittest.TestCase):
    def _measurement(self, layout, read_bytes, marks, memory, workload="numeric_filter"):
        return probe.Measurement(
            layout=layout,
            workload=workload,
            read_bytes=read_bytes,
            selected_marks=marks,
            peak_memory=memory,
            duration_ms=10.0,
            result_check="49000",
        )

    def test_candidate_passes_only_when_all_metrics_within_tolerance(self):
        baseline = self._measurement("legacy", 1000, 40, 300)
        better = self._measurement("dedicated", 800, 20, 250)
        decision = probe.verdict(baseline, better, tolerance=0.02)
        self.assertTrue(decision["passed"])
        self.assertLess(decision["metrics"]["read_bytes"]["ratio"], 1)

    def test_any_regression_fails_the_gate(self):
        baseline = self._measurement("legacy", 1000, 40, 300)
        memory_regression = self._measurement("map", 900, 30, 450)
        decision = probe.verdict(baseline, memory_regression, tolerance=0.02)
        self.assertFalse(decision["passed"])
        self.assertFalse(decision["metrics"]["peak_memory"]["within_tolerance"])
        self.assertTrue(decision["metrics"]["read_bytes"]["within_tolerance"])

    def test_tolerance_absorbs_small_measurement_noise(self):
        baseline = self._measurement("legacy", 1000, 40, 300)
        noisy = self._measurement("dedicated", 1015, 40, 300)  # +1.5% read bytes
        self.assertTrue(probe.verdict(baseline, noisy, tolerance=0.02)["passed"])
        outside = self._measurement("dedicated", 1030, 40, 300)  # +3%
        self.assertFalse(probe.verdict(baseline, outside, tolerance=0.02)["passed"])

    def test_mark_gate_accepts_granule_quantization_but_rejects_structural_widening(self):
        baseline = self._measurement("legacy", 1000, 27, 300)
        one_extra_mark = self._measurement("dedicated", 10, 28, 20)  # +1 granule
        self.assertTrue(probe.verdict(baseline, one_extra_mark)["passed"])
        doubled_marks = self._measurement("map", 10, 59, 400)  # 2.18x granules
        decision = probe.verdict(baseline, doubled_marks)
        self.assertFalse(decision["passed"])
        self.assertFalse(decision["metrics"]["selected_marks"]["within_tolerance"])

    def test_mark_absolute_allowance_does_not_mask_large_regression(self):
        baseline = self._measurement("legacy", 1000, 10, 300)
        # Three extra marks exceeds the absolute allowance even at small counts.
        candidate = self._measurement("map", 10, 13, 300)
        self.assertFalse(probe.verdict(baseline, candidate)["passed"])

    def test_zero_baseline_requires_zero_candidate(self):
        baseline = self._measurement("legacy", 0, 0, 0)
        candidate = self._measurement("dedicated", 1, 0, 0)
        self.assertFalse(probe.verdict(baseline, candidate)["passed"])

    def test_cross_workload_comparison_is_rejected(self):
        baseline = self._measurement("legacy", 1000, 40, 300, "numeric_filter")
        candidate = self._measurement("map", 800, 20, 250, "string_group")
        with self.assertRaises(probe.ProbeError):
            probe.verdict(baseline, candidate)


class TokenDerivationTest(unittest.TestCase):
    def test_encoded_key_matches_go_kernel(self):
        self.assertEqual(probe.encode_key("metric_07"), "p_6D65747269635F3037")
        self.assertEqual(probe.encode_key("segment_03"), "p_7365676D656E745F3033")

    def test_tokens_keep_lexical_spelling(self):
        document = json.dumps(
            {"n": 42, "s": "Home", "flag": False, "arr": [1, "1.00"], "exp": 1e2},
            separators=(",", ":"),
        )
        tokens = probe.derive_tokens(document)
        self.assertEqual(tokens[probe.encode_key("n")], "42")
        self.assertEqual(tokens[probe.encode_key("s")], '"Home"')
        self.assertEqual(tokens[probe.encode_key("flag")], "false")
        self.assertEqual(tokens[probe.encode_key("arr")], '[1,"1.00"]')
        self.assertEqual(tokens[probe.encode_key("exp")], "100.0")

    def test_generated_row_tokens_round_trip_through_map_lookup(self):
        row = probe.generate_row(51)
        tokens = probe.derive_tokens(row["properties_json"])
        numeric_token = tokens[probe.encode_key(probe.NUMERIC_KEY)]
        self.assertEqual(int(numeric_token), 51)
        string_token = tokens[probe.encode_key(probe.HOT_STRING_KEY)]
        self.assertTrue(string_token.startswith('"') and string_token.endswith('"'))


if __name__ == "__main__":
    unittest.main()
