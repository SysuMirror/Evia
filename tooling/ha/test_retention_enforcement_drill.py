import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from retention_enforcement_drill import (
    DrillError,
    evaluate_enforcement,
    parse_retention_summary,
)


def retention_summary(**overrides):
    base = {
        "type": "retention_enforcement_summary",
        "configured_scopes": 6,
        "policy_less_scopes": 1,
        "aged_rows_per_scope": 1500,
        "recent_rows_per_scope": 400,
        "policy_less_rows": 800,
        "total_inserted": 12200,
        "dry_run_candidates": 9000,
        "enforced_deleted": 9000,
        "rows_after_sweep": 3200,
        "retained_recent_rows": 2400,
        "mutations_total": 6,
        "mutations_done": 6,
        "resweep_mutations": 0,
        "table_bytes_before": 250823,
        "table_bytes_peak": 1623401,
        "table_bytes_after": 102634,
        "table_bytes_resweep": 102634,
        "reclaimed_bytes": 148189,
        "peak_amplification": 1372578,
        "disk_free_before": 506613760,
        "disk_free_after": 503885824,
        "part_log": {"MutatePart": {"parts": 6, "bytes": 375940}},
        "audit_records": {"dry_run": 6, "enforced": 12},
        "isolation_intact": True,
        "policy_less_intact": True,
    }
    base.update(overrides)
    return base


class EvaluateEnforcementTest(unittest.TestCase):
    def test_happy_summary_passes(self):
        verdict = evaluate_enforcement(retention_summary())
        self.assertEqual(verdict["deleted_rows"], 9000)
        self.assertEqual(verdict["retained_rows"], 3200)
        self.assertEqual(verdict["mutations"], "6/6")
        self.assertEqual(verdict["resweep_mutations"], 0)
        self.assertTrue(verdict["isolation_intact"])
        self.assertGreater(verdict["peak_to_baseline_ratio"], 1.0)

    def test_missing_field_fails_closed(self):
        broken = retention_summary()
        del broken["reclaimed_bytes"]
        with self.assertRaises(DrillError):
            evaluate_enforcement(broken)

    def test_wrong_type_fails(self):
        with self.assertRaises(DrillError):
            evaluate_enforcement(retention_summary(type="other"))

    def test_dry_run_miscount_fails(self):
        with self.assertRaises(DrillError):
            evaluate_enforcement(retention_summary(dry_run_candidates=8999))

    def test_enforced_mismatch_fails(self):
        with self.assertRaises(DrillError):
            evaluate_enforcement(retention_summary(enforced_deleted=9001))

    def test_retained_reconciliation_fails(self):
        with self.assertRaises(DrillError):
            evaluate_enforcement(retention_summary(rows_after_sweep=3199))

    def test_inserted_reconciliation_fails(self):
        with self.assertRaises(DrillError):
            evaluate_enforcement(retention_summary(total_inserted=12199))

    def test_mutation_count_mismatch_fails(self):
        with self.assertRaises(DrillError):
            evaluate_enforcement(retention_summary(mutations_total=5))
        with self.assertRaises(DrillError):
            evaluate_enforcement(retention_summary(mutations_done=5))

    def test_resweep_mutation_fails(self):
        with self.assertRaises(DrillError):
            evaluate_enforcement(retention_summary(resweep_mutations=1))

    def test_non_positive_reclamation_fails(self):
        with self.assertRaises(DrillError):
            evaluate_enforcement(retention_summary(
                table_bytes_before=100, table_bytes_after=100, reclaimed_bytes=0,
                table_bytes_peak=200, peak_amplification=100))

    def test_reclaimed_bytes_inconsistency_fails(self):
        with self.assertRaises(DrillError):
            evaluate_enforcement(retention_summary(reclaimed_bytes=148190))

    def test_peak_below_baseline_fails(self):
        with self.assertRaises(DrillError):
            evaluate_enforcement(retention_summary(
                table_bytes_peak=250000, peak_amplification=-823))

    def test_resweep_drift_fails(self):
        with self.assertRaises(DrillError):
            evaluate_enforcement(retention_summary(table_bytes_resweep=102635))

    def test_part_log_is_soft_evidence(self):
        # Flush timing may hide events: zero or partial counts must not fail.
        verdict = evaluate_enforcement(
            retention_summary(part_log={"MutatePart": {"parts": 2, "bytes": 1}}))
        self.assertEqual(verdict["part_log_mutate_parts"], 2)
        verdict_zero = evaluate_enforcement(retention_summary(part_log={}))
        self.assertEqual(verdict_zero["part_log_mutate_parts"], 0)

    def test_part_log_accepts_many_parts_per_mutation(self):
        # One mutation rewrites every matching part; counts beyond the scope
        # number are normal and must not gate the drill.
        verdict = evaluate_enforcement(
            retention_summary(part_log={"MutatePart": {"parts": 24, "bytes": 999}}))
        self.assertEqual(verdict["part_log_mutate_parts"], 24)

    def test_part_log_wrong_type_fails(self):
        with self.assertRaises(DrillError):
            evaluate_enforcement(retention_summary(part_log=[]))

    def test_audit_shape_fails(self):
        with self.assertRaises(DrillError):
            evaluate_enforcement(retention_summary(audit_records={"dry_run": 6, "enforced": 6}))
        with self.assertRaises(DrillError):
            evaluate_enforcement(retention_summary(audit_records={"dry_run": 5, "enforced": 12}))

    def test_isolation_violation_fails(self):
        with self.assertRaises(DrillError):
            evaluate_enforcement(retention_summary(isolation_intact=False))
        with self.assertRaises(DrillError):
            evaluate_enforcement(retention_summary(policy_less_intact=False))

    def test_non_dict_summary_fails(self):
        with self.assertRaises(DrillError):
            evaluate_enforcement(None)


class ParseSummaryTest(unittest.TestCase):
    def test_finds_summary_ignoring_spacing(self):
        line = '{"type": "retention_enforcement_summary", "configured_scopes": 6}'
        parsed = parse_retention_summary("=== RUN\n" + line + "\nPASS\n")
        self.assertEqual(parsed["configured_scopes"], 6)

    def test_missing_returns_none(self):
        self.assertIsNone(parse_retention_summary("ok\tgithub.com/example\t1s"))


if __name__ == "__main__":
    unittest.main()
