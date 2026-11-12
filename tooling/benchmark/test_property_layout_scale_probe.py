import unittest

import property_layout_scale_probe as g3
from property_layout_probe import encode_key


class ScaleDDLTest(unittest.TestCase):
    def test_each_layout_ddl_is_distinct_and_safe(self):
        tables = g3.scale_tables()
        self.assertEqual(set(tables), set(g3.SCALE_LAYOUTS))
        ddl = g3.scale_ddl("dedicated_token", tables["dedicated_token"])
        self.assertIn("metric_07_token String", ddl)
        self.assertIn("segment_03_token String", ddl)
        with self.assertRaises(g3.ProbeError):
            g3.scale_ddl("unknown", "t")

    def test_row_payload_keeps_layout_contract(self):
        from property_layout_probe import generate_row

        row = generate_row(7)
        legacy = g3._row_payload("legacy", row)
        self.assertEqual(set(legacy), {"id", "properties_json"})

        mapped = g3._row_payload("map", row)
        self.assertEqual(mapped["property_layout_version"], 1)
        self.assertIn(encode_key("metric_07"), mapped["property_tokens"])

        dedicated = g3._row_payload("dedicated", row)
        self.assertEqual(dedicated["metric_07"], row["metric_value"])

        token = g3._row_payload("dedicated_token", row)
        self.assertIn("metric_07_token", token)


class MatrixQueryTest(unittest.TestCase):
    def setUp(self):
        self.tables = g3.scale_tables()

    def test_count_and_unique_are_layout_invariant(self):
        for layout in g3.SCALE_LAYOUTS:
            count_sql = g3.matrix_query(layout, self.tables[layout], "count")
            unique_sql = g3.matrix_query(layout, self.tables[layout], "unique")
            self.assertTrue(count_sql.startswith("SELECT count() FROM "))
            self.assertIn("uniqExact(id)", unique_sql)

    def test_numeric_filter_uses_layout_storage_path(self):
        legacy = g3.matrix_query("legacy", self.tables["legacy"], "numeric_filter")
        self.assertIn("JSONExtractInt(properties_json, 'metric_07') > 50", legacy)

        mapped = g3.matrix_query("map", self.tables["map"], "numeric_filter")
        self.assertIn(f"property_tokens['{encode_key('metric_07')}']", mapped)

        dedicated = g3.matrix_query("dedicated", self.tables["dedicated"], "numeric_filter")
        self.assertEqual(dedicated, "SELECT count() FROM scale_dedicated WHERE metric_07 > 50")

        token = g3.matrix_query("dedicated_token", self.tables["dedicated_token"], "numeric_filter")
        self.assertIn("concat('{\"f\":',metric_07_token,'}')", token)

    def test_string_group_materializes_distinct_values_in_subquery(self):
        for layout in g3.SCALE_LAYOUTS:
            sql = g3.matrix_query(layout, self.tables[layout], "string_group")
            self.assertIn("GROUP BY g", sql)
            self.assertTrue(sql.startswith("SELECT count() FROM ("))

    def test_unknown_shape_rejected(self):
        with self.assertRaises(g3.ProbeError):
            g3.matrix_query("legacy", self.tables["legacy"], "nope")


class SummarizeTest(unittest.TestCase):
    def test_summary_flags_memory_budget_breach_and_failures(self):
        outcomes = [
            g3.QueryOutcome("count", "legacy", 0, True, "", 100, 1, 1024, 5.0, "50000"),
            g3.QueryOutcome("count", "map", 0, False, "error 243", 400, 2,
                            g3.MEMORY_BUDGET_BYTES + 1, 9.0, ""),
        ]
        summary = g3.summarize(outcomes)
        self.assertTrue(summary["count.legacy"]["within_memory_budget"])
        self.assertEqual(summary["count.legacy"]["successes"], 1)
        self.assertFalse(summary["count.map"]["within_memory_budget"])
        self.assertEqual(summary["count.map"]["successes"], 0)
        self.assertIn("error 243", summary["count.map"]["errors"][0])

    def test_run_matrix_rejects_out_of_range_concurrency(self):
        with self.assertRaises(g3.ProbeError):
            g3.run_matrix(None, {}, 0, 1)
        with self.assertRaises(g3.ProbeError):
            g3.run_matrix(None, {}, 17, 1)


    def test_cross_layout_equivalence_compares_distinct_results(self):
        outcomes = []
        for layout in g3.SCALE_LAYOUTS:
            outcomes.append(g3.QueryOutcome("numeric_filter", layout, 0, True, "",
                                            10, 1, 1024, 5.0, "24500"))
        summary = g3.summarize(outcomes)
        self.assertTrue(summary["_cross_layout_equivalence"]["numeric_filter"])

        # A divergent result on one layout breaks equivalence rather than
        # being silently treated as equal.
        outcomes[0] = g3.QueryOutcome("numeric_filter", "legacy", 0, True, "",
                                      10, 1, 1024, 5.0, "999")
        summary = g3.summarize(outcomes)
        self.assertFalse(summary["_cross_layout_equivalence"]["numeric_filter"])


if __name__ == "__main__":
    unittest.main()
