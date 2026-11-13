import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from retention_headroom_probe import (
    AGED_DAYS,
    SCOPE_COUNT,
    Cell,
    CellMeasurement,
    PartsBytePoller,
    ProbeError,
    allocate_bands,
    build_row,
    cell_database,
    delete_mutation_sql,
    evaluate_cell,
    format_datetime,
    headroom_ratio,
    plan_cells,
    reclaimed_fraction,
    scope_pairs,
    scope_predicate,
    split_sql_migrations,
    validate_environment,
    validate_probe_database,
    validate_uuid,
)


class ValidationTest(unittest.TestCase):
    def test_probe_database_closed_set(self):
        validate_probe_database("evia_probe_ret_s100000_f25")
        for bad in ("evia_pipeline_x", "evia_probe_ret_s100000_f25;DROP",
                    "evia_probe_ret_", "evia_probe_other_x"):
            with self.assertRaises(ProbeError):
                validate_probe_database(bad)

    def test_uuid_validation(self):
        validate_uuid("44444444-4444-4444-8444-444444444444")
        for bad in ("not-a-uuid", "", "44444444-4444-4444-8444-44444444444z"):
            with self.assertRaises(ProbeError):
                validate_uuid(bad)

    def test_environment_validation(self):
        validate_environment("development")
        validate_environment("staging")
        validate_environment("production")
        with self.assertRaises(ProbeError):
            validate_environment("admin")

    def test_cell_database_deterministic(self):
        self.assertEqual(cell_database(100_000, 0.25), "evia_probe_ret_s100000_f25")
        self.assertEqual(cell_database(500_000, 0.75), "evia_probe_ret_s500000_f75")

    def test_datetime_format(self):
        literal = format_datetime(0.0)
        self.assertRegex(literal, r"^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3}$")


class MatrixPlanTest(unittest.TestCase):
    def test_plans_cartesian_product(self):
        cells = plan_cells((100_000, 500_000), (0.25, 0.5, 0.75))
        self.assertEqual(len(cells), 6)
        self.assertEqual(cells[0].database, "evia_probe_ret_s100000_f25")
        self.assertEqual(cells[-1].database, "evia_probe_ret_s500000_f75")

    def test_band_totals_reconcile(self):
        for cell in plan_cells((100_000, 500_000), (0.25, 0.5, 0.75)):
            self.assertEqual(cell.aged_rows + cell.recent_rows, cell.rows)

    def test_rejects_bad_scale(self):
        # 6 rows at 50% leaves a band under the scope count.
        with self.assertRaises(ProbeError):
            plan_cells((6,), (0.5,))
        with self.assertRaises(ProbeError):
            plan_cells((-6,), (0.5,))

    def test_rejects_bad_fraction(self):
        for fraction in (0.0, 1.0, -0.1, 1.5):
            with self.assertRaises(ProbeError):
                plan_cells((100_000,), (fraction,))


class AllocateBandsTest(unittest.TestCase):
    def test_every_row_assigned_once(self):
        cell = Cell(100_000, 0.25)
        bands = allocate_bands(cell)
        self.assertEqual(len(bands), SCOPE_COUNT)
        self.assertEqual(sum(aged for aged, _ in bands), cell.aged_rows)
        self.assertEqual(sum(recent for _, recent in bands), cell.recent_rows)

    def test_remainder_goes_to_lowest_scopes(self):
        # 100000 * 0.25 = 25000 -> 4166 per scope + remainder 4 to first four
        cell = Cell(100_000, 0.25)
        bands = allocate_bands(cell)
        self.assertEqual([aged for aged, _ in bands[:4]], [4167] * 4)
        self.assertEqual([aged for aged, _ in bands[4:]], [4166] * 2)

    def test_every_scope_keeps_both_bands(self):
        for cell in plan_cells((100_000, 500_000), (0.25, 0.5, 0.75)):
            for aged, recent in allocate_bands(cell):
                self.assertGreaterEqual(aged, 1)
                self.assertGreaterEqual(recent, 1)

    def test_scope_count_is_closed(self):
        with self.assertRaises(ProbeError):
            allocate_bands(Cell(100_000, 0.5), scopes=5)


class SqlBuilderTest(unittest.TestCase):
    CUTOFF = "2026-10-15 00:00:00.000"

    def test_predicate_mirrors_executor_shape(self):
        predicate = scope_predicate(
            "44444444-4444-4444-8444-444444444444", "production", self.CUTOFF)
        self.assertEqual(
            predicate,
            "project_id='44444444-4444-4444-8444-444444444444' "
            "AND environment='production' "
            "AND event_time < toDateTime64('2026-10-15 00:00:00.000', 3)")

    def test_predicate_rejects_injection(self):
        good = "44444444-4444-4444-8444-444444444444"
        for bad_project, bad_env, bad_cutoff in (
            ("' OR 1=1 --", "production", self.CUTOFF),
            (good, "prod' OR '1'='1", self.CUTOFF),
            (good, "production", "2026-10-15"),
        ):
            with self.assertRaises(ProbeError):
                scope_predicate(bad_project, bad_env, bad_cutoff)

    def test_mutation_sql_uses_sync_setting(self):
        sql = delete_mutation_sql(
            "evia_probe_ret_s100000_f25",
            scope_predicate("44444444-4444-4444-8444-444444444444",
                            "development", self.CUTOFF))
        self.assertIn("ALTER TABLE `evia_probe_ret_s100000_f25`.`event_facts_raw`", sql)
        self.assertIn("DELETE WHERE", sql)
        self.assertTrue(sql.endswith("SETTINGS mutations_sync=1"))

    def test_mutation_rejects_unknown_database(self):
        with self.assertRaises(ProbeError):
            delete_mutation_sql("evia_pipeline_x", "1=1")

    def test_scope_pairs_closed_set(self):
        pairs = scope_pairs()
        self.assertEqual(len(pairs), SCOPE_COUNT)
        self.assertEqual(len(set(pairs)), SCOPE_COUNT)


class MigrationSplitterTest(unittest.TestCase):
    def test_strips_comments_and_splits(self):
        text = "-- header comment\nCREATE TABLE a (x UInt8);\n-- mid comment\nALTER TABLE a ADD COLUMN y String;\n"
        statements = split_sql_migrations(text)
        self.assertEqual(statements, ["CREATE TABLE a (x UInt8)",
                                      "ALTER TABLE a ADD COLUMN y String"])

    def test_drops_empty_statements(self):
        self.assertEqual(split_sql_migrations("-- only comment\n;\n"), [])
        self.assertEqual(split_sql_migrations(""), [])


class HeadroomMathTest(unittest.TestCase):
    def test_ratio_and_fraction(self):
        self.assertEqual(headroom_ratio(200, 100), 2.0)
        self.assertEqual(reclaimed_fraction(200, 150), 0.25)

    def test_rejects_non_positive_baseline(self):
        with self.assertRaises(ProbeError):
            headroom_ratio(10, 0)
        with self.assertRaises(ProbeError):
            reclaimed_fraction(0, 0)


def measurement(**overrides):
    base = CellMeasurement(
        cell=Cell(100_000, 0.5),
        baseline_bytes=1_000_000,
        baseline_active_parts=4,
        after_bytes=600_000,
        after_active_parts=4,
        mutations_total=SCOPE_COUNT,
        mutations_done=SCOPE_COUNT,
        rows_after=50_000,
        expired_after=0,
        recent_after=50_000,
        disk_free_before=10_000_000,
        disk_free_after=10_500_000,
    )
    from retention_headroom_probe import ScopeMeasurement
    for index in range(SCOPE_COUNT):
        base.scopes.append(ScopeMeasurement(
            f"44444444-4444-4444-8444-44444444444{index}", "development",
            1_500_000, 1200))
    for key, value in overrides.items():
        setattr(base, key, value)
    return base


class EvaluateCellTest(unittest.TestCase):
    def test_happy_passes(self):
        verdict = evaluate_cell(measurement())
        self.assertTrue(verdict["passed"])
        self.assertEqual(verdict["failures"], [])
        self.assertEqual(verdict["max_headroom_ratio"], 1.5)
        self.assertEqual(verdict["reclaimed_fraction"], 0.4)

    def test_mutation_shortfall_fails(self):
        self.assertFalse(evaluate_cell(measurement(mutations_done=5))["passed"])
        self.assertFalse(evaluate_cell(measurement(mutations_total=5))["passed"])

    def test_expired_residue_fails(self):
        self.assertFalse(evaluate_cell(measurement(expired_after=7))["passed"])

    def test_recent_loss_fails(self):
        self.assertFalse(evaluate_cell(measurement(recent_after=49_999))["passed"])

    def test_no_shrink_fails(self):
        self.assertFalse(
            evaluate_cell(measurement(after_bytes=1_000_000))["passed"])

    def test_peak_below_baseline_fails(self):
        from retention_headroom_probe import ScopeMeasurement
        weak = measurement()
        weak.scopes = [ScopeMeasurement("p", "development", 900_000, 10)] * SCOPE_COUNT
        self.assertFalse(evaluate_cell(weak)["passed"])


class RowBuilderTest(unittest.TestCase):
    def test_row_shape_and_deterministic_hash(self):
        row = build_row("evt-1", "44444444-4444-4444-8444-444444444444",
                        "production", "2026-01-01 00:00:00.000",
                        "2026-11-14 00:00:00.000000000")
        self.assertEqual(row["project_id"], "44444444-4444-4444-8444-444444444444")
        self.assertEqual(len(row["canonical_hash"]), 64)
        again = build_row("evt-1", row["project_id"], "production",
                          row["event_time"], row["received_at"])
        self.assertEqual(again["canonical_hash"], row["canonical_hash"])
        self.assertIsNone(row["anonymous_id"])


class PollerConstructTest(unittest.TestCase):
    def test_poller_holds_database(self):
        poller = PartsBytePoller.__new__(PartsBytePoller)
        # Construction validation only; no client calls in this unit test.
        with self.assertRaises(ProbeError):
            PartsBytePoller(None, "evia_pipeline_x")


if __name__ == "__main__":
    unittest.main()
