import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from property_layout_probe import ProbeError
from query_guard_probe import (
    CODE_CANCELLED,
    CODE_READONLY,
    CODE_RESULT_LIMIT,
    CODE_TIMEOUT,
    GuardOutcome,
    CancellationResult,
    LABEL_CANCELLED,
    LABEL_OK,
    LABEL_OTHER,
    LABEL_READONLY,
    LABEL_RESULT_BYTES,
    LABEL_RESULT_ROWS,
    LABEL_TIMEOUT,
    classify_guard,
    evaluate_cancellation,
    evaluate_guard_outcome,
    extract_exception_code,
    extract_exception_type,
    kill_query_sql,
    long_running_sql,
    readonly_write_sql,
    result_bytes_overflow_sql,
    result_rows_overflow_sql,
    sibling_sql,
    timeout_sql,
    with_production_settings,
)

READONLY_MSG = ("Code: 164. DB::Exception: Cannot execute query in readonly "
                "mode. (READONLY)")
TIMEOUT_MSG = ("Code: 159. DB::Exception: Timeout exceeded: elapsed 1000 ms, "
               "maximum: 1000 ms. (TIMEOUT_EXCEEDED)")
RESULT_ROWS_MSG = ("Code: 396. DB::Exception: Limit for result exceeded, max "
                   "rows: 10, current rows: 1000. (TOO_MANY_ROWS_OR_BYTES)")
RESULT_BYTES_MSG = ("Code: 396. DB::Exception: Limit for result exceeded, max "
                    "bytes: 1 KiB, current bytes: 10 MiB. (TOO_MANY_ROWS_OR_BYTES)")
CANCEL_MSG = "Code: 394. DB::Exception: Query was cancelled. (QUERY_WAS_CANCELLED)"


class ExtractionTest(unittest.TestCase):
    def test_extract_code(self):
        self.assertEqual(extract_exception_code(READONLY_MSG), CODE_READONLY)
        self.assertIsNone(extract_exception_code("no code"))

    def test_extract_type(self):
        self.assertEqual(extract_exception_type(TIMEOUT_MSG), "TIMEOUT_EXCEEDED")
        self.assertIsNone(extract_exception_type("no type"))


class ClassificationTest(unittest.TestCase):
    def test_readonly(self):
        self.assertEqual(classify_guard(READONLY_MSG), LABEL_READONLY)

    def test_timeout(self):
        self.assertEqual(classify_guard(TIMEOUT_MSG), LABEL_TIMEOUT)

    def test_cancelled(self):
        self.assertEqual(classify_guard(CANCEL_MSG), LABEL_CANCELLED)

    def test_result_code_reuse_disambiguated_by_text(self):
        # Same code 396, distinct labels based on max rows vs max bytes.
        self.assertEqual(classify_guard(RESULT_ROWS_MSG), LABEL_RESULT_ROWS)
        self.assertEqual(classify_guard(RESULT_BYTES_MSG), LABEL_RESULT_BYTES)

    def test_code_fallback_when_type_missing(self):
        self.assertEqual(
            classify_guard("Code: 164. DB::Exception: blocked"), LABEL_READONLY)
        self.assertEqual(
            classify_guard("Code: 394. DB::Exception: stopped"), LABEL_CANCELLED)

    def test_unknown_is_other(self):
        self.assertEqual(classify_guard("Code: 999. weird (SOMETHING_NEW)"),
                         LABEL_OTHER)
        self.assertEqual(classify_guard("plain failure"), LABEL_OTHER)

    def test_type_name_takes_precedence_over_reused_code(self):
        # A 396 carrying the timeout type name must be classified by type.
        message = "Code: 396. x (TIMEOUT_EXCEEDED)"
        self.assertEqual(classify_guard(message), LABEL_TIMEOUT)


class SqlBuilderTest(unittest.TestCase):
    def test_every_builder_carries_production_settings(self):
        for sql in (readonly_write_sql(), timeout_sql(),
                    result_rows_overflow_sql(), result_bytes_overflow_sql(),
                    sibling_sql()):
            self.assertIn("SETTINGS ", sql)
            self.assertIn("readonly=2", sql)
            self.assertIn("max_memory_usage=536870912", sql)

    def test_readonly_builder_is_a_write(self):
        self.assertIn("CREATE TABLE", readonly_write_sql())
        self.assertIn("ENGINE=Memory", readonly_write_sql())

    def test_result_builders_target_distinct_limits(self):
        self.assertIn("numbers(200000)", result_rows_overflow_sql())
        self.assertIn("groupArray", result_bytes_overflow_sql())

    def test_timeout_overrides_only_time_budget(self):
        sql = timeout_sql()
        self.assertIn("sleep(2)", sql)
        # Overridden to one second for a deterministic test, while the read
        # cap and other production guards remain in place.
        self.assertIn("max_execution_time=1", sql)
        self.assertIn("max_rows_to_read=2000000", sql)

    def test_kill_sql_closed_set(self):
        sql = kill_query_sql("evia-guard-long")
        self.assertIn("KILL QUERY WHERE query_id='evia-guard-long' ASYNC", sql)
        for bad in ("evia-guard-'; DROP", "a b", "../x"):
            with self.assertRaises(ProbeError):
                kill_query_sql(bad)

    def test_long_running_id_closed_set(self):
        self.assertIn("max_execution_time=120", long_running_sql("evia-guard-long"))
        with self.assertRaises(ProbeError):
            long_running_sql("bad id")

    def test_settings_helper_equivalence(self):
        rendered = with_production_settings("SELECT 1")
        self.assertTrue(rendered.startswith("SELECT 1 SETTINGS "))
        self.assertIn("readonly=2", rendered)
        self.assertIn("max_execution_time=30", rendered)


class GuardVerdictTest(unittest.TestCase):
    def test_matching_rejection_passes(self):
        outcome = GuardOutcome("g", False, LABEL_READONLY, CODE_READONLY, 5.0)
        self.assertEqual(evaluate_guard_outcome(outcome, LABEL_READONLY), [])

    def test_successful_guard_fails(self):
        outcome = GuardOutcome("g", True, LABEL_OK, None, 5.0)
        self.assertTrue(evaluate_guard_outcome(outcome, LABEL_READONLY))

    def test_misclassified_fails(self):
        outcome = GuardOutcome("g", False, LABEL_OTHER, 999, 5.0)
        failures = evaluate_guard_outcome(outcome, LABEL_TIMEOUT)
        self.assertTrue(failures)


class CancellationVerdictTest(unittest.TestCase):
    def test_happy_passes(self):
        result = CancellationResult(
            long_query_seen_before_kill=True, process_gone_after_kill=True,
            long_query_label=LABEL_CANCELLED, long_query_code=CODE_CANCELLED,
            sibling_succeeded=True, sibling_label=LABEL_OK)
        self.assertEqual(evaluate_cancellation(result), [])

    def test_not_seen_fails(self):
        result = CancellationResult(False, True, LABEL_CANCELLED, CODE_CANCELLED,
                                    True, LABEL_OK)
        self.assertTrue(evaluate_cancellation(result))

    def test_process_remains_fails(self):
        result = CancellationResult(True, False, LABEL_CANCELLED, CODE_CANCELLED,
                                    True, LABEL_OK)
        self.assertTrue(evaluate_cancellation(result))

    def test_wrong_terminal_label_fails(self):
        result = CancellationResult(True, True, LABEL_TIMEOUT, CODE_TIMEOUT,
                                    True, LABEL_OK)
        self.assertTrue(evaluate_cancellation(result))

    def test_sibling_casualty_fails(self):
        result = CancellationResult(True, True, LABEL_CANCELLED, CODE_CANCELLED,
                                    False, LABEL_OTHER)
        self.assertTrue(evaluate_cancellation(result))


if __name__ == "__main__":
    unittest.main()
