"""Self-tests for the Prometheus rule gate; run via python3 tooling/test_check_monitoring.py."""
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))

import check_monitoring as gate  # noqa: E402


class ReferencedMetricsTest(unittest.TestCase):
    def test_extracts_plain_metric_and_strips_duration(self):
        self.assertEqual(
            gate.referenced_metrics("rate(evia_ingest_batches_total[5m])"),
            {"evia_ingest_batches_total"},
        )

    def test_keeps_recording_rule_suffixes(self):
        expr = "evia:slo_http_requests:rate5m > 0.2 and evia:slo_http_requests:rate5m < 1"
        self.assertEqual(gate.referenced_metrics(expr), {"evia:slo_http_requests:rate5m"})

    def test_drops_label_matchers_strings_and_aggregations(self):
        expr = 'sum by (job) (rate(evia_http_requests_total{code=~"5..",job="evia-x"}[1h]))'
        self.assertEqual(gate.referenced_metrics(expr), {"evia_http_requests_total"})

    def test_nested_balanced_braces(self):
        self.assertEqual(gate.strip_balanced("a{x{y}z}b", "{", "}"), "ab")


class EmittedMetricsTest(unittest.TestCase):
    def test_production_metrics_present_and_test_metrics_excluded(self):
        names = gate.emitted_metrics()
        self.assertIn("evia_http_requests_total", names)
        self.assertIn("up", names)
        self.assertIn("evia_http_request_duration_seconds_bucket", names)
        self.assertIn("go_goroutines", names)
        self.assertIn("process_start_time_seconds", names)
        self.assertNotIn("evia_test_events_total", names)


class GateEndToEndTest(unittest.TestCase):
    def test_current_rules_pass(self):
        gate.main()

    def test_unknown_metric_fails(self):
        broken = """
groups:
  - name: evia-broken
    rules:
      - record: evia:slo_broken:ratio5m
        expr: sum(rate(evia_does_not_exist_total[5m]))
"""
        with tempfile.TemporaryDirectory() as tmp:
            rules = Path(tmp) / "rules.yaml"
            rules.write_text(broken, encoding="utf-8")
            with mock.patch.object(gate, "RULES_FILE", rules), mock.patch.object(
                gate, "RULE_TEST_FILE", Path(tmp) / "missing.test.yaml"
            ):
                with self.assertRaises(SystemExit) as caught:
                    gate.main()
        self.assertEqual(caught.exception.code, 1)


if __name__ == "__main__":
    unittest.main()
