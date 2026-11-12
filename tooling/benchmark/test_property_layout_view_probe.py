import unittest

import property_layout_view_probe as g2
from property_layout_probe import encode_key


class ViewTupleColumnsTest(unittest.TestCase):
    def test_layouts_carry_distinct_column_sets(self):
        self.assertEqual(g2.view_tuple_columns("legacy"), [])
        self.assertEqual(g2.view_tuple_columns("map"), ["property_tokens"])
        self.assertEqual(g2.view_tuple_columns("dedicated"), ["metric_07", "segment_03"])
        self.assertEqual(
            g2.view_tuple_columns("dedicated_token"),
            ["metric_07_token", "segment_03_token"],
        )

    def test_unknown_layout_rejected(self):
        with self.assertRaises(g2.ProbeError):
            g2.view_tuple_columns("nope")


class ViewDDLTest(unittest.TestCase):
    def test_each_view_uses_argmin_tuple_with_layout_columns(self):
        ddl = g2.view_ddl("map", "db.probe_map")
        self.assertIn("CREATE VIEW probe_view_map", ddl)
        self.assertIn("argMin(tuple(properties_json,property_tokens), tuple(id))", ddl)
        self.assertIn("GROUP BY id", ddl)

    def test_legacy_tuple_has_only_document(self):
        ddl = g2.view_ddl("legacy", "db.probe_legacy")
        self.assertIn("argMin(tuple(properties_json), tuple(id))", ddl)

    def test_unsafe_identifier_rejected(self):
        with self.assertRaises(g2.ProbeError):
            g2.view_ddl("map; DROP", "db.probe_map")
        with self.assertRaises(g2.ProbeError):
            g2.view_ddl("map", "db; DROP")


class ViewTrendQueryTest(unittest.TestCase):
    def test_all_queries_target_their_own_view(self):
        for layout in g2.VIEW_LAYOUTS:
            sql = g2.view_trend_query(layout)
            self.assertIn(f"FROM probe_view_{layout} ", sql)
            self.assertIn("> 50", sql)

    def test_legacy_reads_winner_document(self):
        sql = g2.view_trend_query("legacy")
        self.assertIn("JSONExtractInt(winner.1, 'metric_07') > 50", sql)

    def test_map_reads_encoded_token_from_winner_two(self):
        sql = g2.view_trend_query("map")
        key = encode_key("metric_07")
        self.assertIn(f"winner.2['{key}']", sql)

    def test_dedicated_reads_typed_slot_directly(self):
        sql = g2.view_trend_query("dedicated")
        self.assertEqual(sql, "SELECT count() FROM probe_view_dedicated WHERE winner.2 > 50")

    def test_token_slot_uses_synthetic_document(self):
        sql = g2.view_trend_query("dedicated_token")
        self.assertIn("JSONExtractInt(concat('{\"f\":',winner.2,'}'), 'f') > 50", sql)


if __name__ == "__main__":
    unittest.main()
