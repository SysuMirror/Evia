import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from tenant_query_isolation_probe import (
    CODE_MEMORY_LIMIT_EXCEEDED,
    CODE_TOO_MANY_BYTES,
    CODE_TOO_MANY_ROWS,
    LABEL_MEMORY,
    LABEL_OK,
    LABEL_OTHER,
    LABEL_READ_BYTES,
    LABEL_READ_ROWS,
    LABEL_RESULT,
    MEMORY_BUDGET_BYTES,
    PRODUCTION_QUERY_SETTINGS,
    QueryOutcome,
    ScenarioResult,
    aggregate_memory_ceiling,
    assert_settings_match_production,
    bounded_count_sql,
    bounded_unique_sql,
    classify_exception,
    evaluate_bounded_concurrency,
    evaluate_guard_rejection,
    evaluate_noisy_neighbor,
    extract_exception_code,
    heavy_memory_sql,
    heavy_read_rows_sql,
    percentile,
    settings_clause,
    summarize_scenario,
    tenant_name,
    validate_probe_database,
    validate_tenant,
    with_settings,
)
from property_layout_probe import ProbeError

TABLE_QUALIFIED = "evia_probe_ten_default.tenant_events"


class IdentifierTest(unittest.TestCase):
    def test_tenant_name_closed_set(self):
        self.assertEqual(tenant_name(0), "tenant_000000")
        self.assertEqual(tenant_name(255), "tenant_0000ff")
        validate_tenant("tenant_000000")
        for bad in ("tenant_x", "tenant_00000", "000000", ""):
            with self.assertRaises(ProbeError):
                validate_tenant(bad)

    def test_tenant_index_range(self):
        with self.assertRaises(ProbeError):
            tenant_name(-1)

    def test_probe_database_closed_set(self):
        validate_probe_database("evia_probe_ten_default")
        for bad in ("evia_pipeline_x", "evia_probe_ten_x;DROP", "evia_probe_other"):
            with self.assertRaises(ProbeError):
                validate_probe_database(bad)


class SettingsTest(unittest.TestCase):
    def test_clause_renders_all_keys(self):
        clause = settings_clause()
        self.assertTrue(clause.startswith("SETTINGS "))
        for key, value in PRODUCTION_QUERY_SETTINGS.items():
            self.assertIn(f"{key}={value}", clause)

    def test_critical_production_limits_present(self):
        self.assertEqual(PRODUCTION_QUERY_SETTINGS["max_memory_usage"],
                         str(MEMORY_BUDGET_BYTES))
        self.assertEqual(PRODUCTION_QUERY_SETTINGS["max_threads"], "2")
        self.assertEqual(PRODUCTION_QUERY_SETTINGS["readonly"], "2")
        self.assertEqual(PRODUCTION_QUERY_SETTINGS["max_rows_to_read"], "2000000")

    def test_drift_detection(self):
        assert_settings_match_production(dict(PRODUCTION_QUERY_SETTINGS))
        drifted = dict(PRODUCTION_QUERY_SETTINGS)
        drifted["max_memory_usage"] = "1024"
        with self.assertRaises(ProbeError):
            assert_settings_match_production(drifted)
        missing = dict(PRODUCTION_QUERY_SETTINGS)
        del missing["readonly"]
        with self.assertRaises(ProbeError):
            assert_settings_match_production(missing)

    def test_with_settings_suffix(self):
        sql = with_settings("SELECT 1")
        self.assertTrue(sql.startswith("SELECT 1 SETTINGS "))
        self.assertIn("max_memory_usage=536870912", sql)


class SqlBuilderTest(unittest.TestCase):
    def test_bounded_queries_scoped_to_tenant(self):
        count_sql = bounded_count_sql(TABLE_QUALIFIED, "tenant_000001")
        unique_sql = bounded_unique_sql(TABLE_QUALIFIED, "tenant_000001")
        self.assertIn("WHERE tenant_id='tenant_000001'", count_sql)
        self.assertIn("uniqExact(seq)", unique_sql)

    def test_bounded_query_rejects_bad_tenant(self):
        with self.assertRaises(ProbeError):
            bounded_count_sql(TABLE_QUALIFIED, "tenant_' OR '1'='1")

    def test_heavy_shapes_are_distinct_and_deterministic(self):
        self.assertIn("groupArray", heavy_memory_sql())
        self.assertIn("numbers(5000000)", heavy_read_rows_sql())
        self.assertEqual(heavy_memory_sql(), heavy_memory_sql())


class ExceptionClassificationTest(unittest.TestCase):
    def test_code_mapping(self):
        self.assertEqual(classify_exception(CODE_MEMORY_LIMIT_EXCEEDED), LABEL_MEMORY)
        self.assertEqual(classify_exception(CODE_TOO_MANY_ROWS), LABEL_READ_ROWS)
        self.assertEqual(classify_exception(CODE_TOO_MANY_BYTES), LABEL_READ_BYTES)
        self.assertEqual(classify_exception(160), LABEL_RESULT)
        self.assertEqual(classify_exception(999), LABEL_OTHER)

    def test_classify_from_message(self):
        message = ("Code: 241. DB::Exception: Query memory limit exceeded: "
                   "would use 552.60 MiB, maximum: 512.00 MiB")
        self.assertEqual(extract_exception_code(message), 241)
        self.assertEqual(classify_exception(None, message), LABEL_MEMORY)

    def test_unparseable_message_is_other(self):
        self.assertEqual(extract_exception_code("no code here"), None)
        self.assertEqual(classify_exception(None, "no code here"), LABEL_OTHER)


class MathHelperTest(unittest.TestCase):
    def test_aggregate_ceiling(self):
        self.assertEqual(aggregate_memory_ceiling(4), 4 * MEMORY_BUDGET_BYTES)
        self.assertEqual(aggregate_memory_ceiling(8), 8 * MEMORY_BUDGET_BYTES)

    def test_aggregate_ceiling_rejects_bad_concurrency(self):
        for bad in (0, -1):
            with self.assertRaises(ProbeError):
                aggregate_memory_ceiling(bad)

    def test_percentile(self):
        values = [1.0, 2.0, 3.0, 4.0, 5.0]
        self.assertEqual(percentile(sorted(values), 50), 3.0)
        self.assertEqual(percentile(sorted(values), 95), 5.0)
        self.assertEqual(percentile(sorted(values), 100), 5.0)
        with self.assertRaises(ProbeError):
            percentile([], 50)


def outcome(kind, succeeded, label, peak_memory=1024, duration=10.0,
            tenant="tenant_000000"):
    return QueryOutcome(
        query_id=f"q-{kind}", tenant=tenant, kind=kind, succeeded=succeeded,
        label=label, exception_code=None, peak_memory=peak_memory,
        duration_ms=duration)


def scenario(name, outcomes, concurrency=4, aggregate_peak=0):
    result = ScenarioResult(name=name, concurrency=concurrency,
                            outcomes=outcomes,
                            aggregate_ceiling=aggregate_memory_ceiling(concurrency))
    result.aggregate_peak_memory = aggregate_peak
    return result


class GuardRejectionEvaluationTest(unittest.TestCase):
    def test_happy_passes(self):
        result = scenario("g", [
            outcome("heavy_memory", False, LABEL_MEMORY),
            outcome("heavy_read_rows", False, LABEL_READ_ROWS)])
        self.assertEqual(evaluate_guard_rejection(result), [])

    def test_wrong_label_fails(self):
        result = scenario("g", [
            outcome("heavy_memory", False, LABEL_OTHER),
            outcome("heavy_read_rows", False, LABEL_READ_ROWS)])
        self.assertTrue(evaluate_guard_rejection(result))

    def test_successful_heavy_fails(self):
        result = scenario("g", [
            outcome("heavy_memory", True, LABEL_OK),
            outcome("heavy_read_rows", False, LABEL_READ_ROWS)])
        self.assertTrue(evaluate_guard_rejection(result))

    def test_missing_heavy_fails(self):
        result = scenario("g", [outcome("heavy_memory", False, LABEL_MEMORY)])
        self.assertTrue(evaluate_guard_rejection(result))


class BoundedConcurrencyEvaluationTest(unittest.TestCase):
    def test_happy_passes(self):
        outcomes = [outcome("bounded", True, LABEL_OK, peak_memory=50_000_000)
                    for _ in range(8)]
        result = scenario("b", outcomes, concurrency=4,
                          aggregate_peak=aggregate_memory_ceiling(4))
        self.assertEqual(evaluate_bounded_concurrency(result), [])

    def test_failed_bounded_fails(self):
        outcomes = [outcome("bounded", True, LABEL_OK)]
        outcomes.append(outcome("bounded", False, LABEL_OTHER))
        self.assertTrue(evaluate_bounded_concurrency(scenario("b", outcomes)))

    def test_over_budget_fails(self):
        outcomes = [outcome("bounded", True, LABEL_OK,
                            peak_memory=MEMORY_BUDGET_BYTES + 1)]
        self.assertTrue(evaluate_bounded_concurrency(scenario("b", outcomes)))

    def test_aggregate_over_ceiling_fails(self):
        outcomes = [outcome("bounded", True, LABEL_OK)]
        result = scenario("b", outcomes, concurrency=2,
                          aggregate_peak=aggregate_memory_ceiling(2) + 1)
        self.assertTrue(evaluate_bounded_concurrency(result))

    def test_empty_fails(self):
        self.assertTrue(evaluate_bounded_concurrency(scenario("b", [])))


class NoisyNeighborEvaluationTest(unittest.TestCase):
    def test_happy_passes(self):
        outcomes = [outcome("bounded", True, LABEL_OK) for _ in range(4)]
        outcomes.append(outcome("heavy_memory", False, LABEL_MEMORY))
        outcomes.append(outcome("heavy_read_rows", False, LABEL_READ_ROWS))
        self.assertEqual(evaluate_noisy_neighbor(scenario("n", outcomes)), [])

    def test_bounded_casualty_fails(self):
        outcomes = [outcome("bounded", True, LABEL_OK),
                    outcome("bounded", False, LABEL_OTHER),
                    outcome("heavy_memory", False, LABEL_MEMORY),
                    outcome("heavy_read_rows", False, LABEL_READ_ROWS)]
        self.assertTrue(evaluate_noisy_neighbor(scenario("n", outcomes)))

    def test_missing_guard_fails(self):
        outcomes = [outcome("bounded", True, LABEL_OK),
                    outcome("heavy_memory", False, LABEL_MEMORY)]
        self.assertTrue(evaluate_noisy_neighbor(scenario("n", outcomes)))

    def test_successful_heavy_fails(self):
        outcomes = [outcome("bounded", True, LABEL_OK),
                    outcome("heavy_memory", True, LABEL_OK),
                    outcome("heavy_read_rows", False, LABEL_READ_ROWS)]
        self.assertTrue(evaluate_noisy_neighbor(scenario("n", outcomes)))


class SummarizeTest(unittest.TestCase):
    def test_summary_shape(self):
        outcomes = [
            outcome("bounded", True, LABEL_OK, peak_memory=100, duration=5.0),
            outcome("bounded", True, LABEL_OK, peak_memory=200, duration=15.0),
            outcome("heavy_memory", False, LABEL_MEMORY)]
        result = scenario("s", outcomes, concurrency=4, aggregate_peak=4096)
        summary = summarize_scenario(result)
        self.assertEqual(summary["bounded_queries"], 2)
        self.assertEqual(summary["bounded_succeeded"], 2)
        self.assertEqual(summary["bounded_peak_memory_max"], 200)
        self.assertEqual(summary["heavy_labels"], [LABEL_MEMORY])
        self.assertGreaterEqual(summary["bounded_duration_p95_ms"],
                                summary["bounded_duration_p50_ms"])


if __name__ == "__main__":
    unittest.main()
