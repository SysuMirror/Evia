"""Self-tests for the Prometheus rule gate; run via python3 tooling/test_check_monitoring.py."""
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

import yaml

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


class DashboardTest(unittest.TestCase):
    def test_walk_finds_nested_exprs(self):
        tree = {"panels": [{"targets": [{"expr": "rate(up[5m])"}]}, {"rows": [{"expr": "evia_test"}]}]}
        self.assertEqual(sorted(gate._walk_exprs(tree)), ["evia_test", "rate(up[5m])"])

    def test_shipped_dashboard_passes(self):
        names = gate.emitted_metrics()
        document = yaml.safe_load(gate.RULES_FILE.read_text(encoding="utf-8"))
        record_names = {
            rule["record"]
            for group in document["groups"]
            for rule in group["rules"]
            if "record" in rule
        }
        self.assertEqual(gate._check_dashboard(names, record_names), [])

    def test_unknown_dashboard_metric_fails(self):
        names = gate.emitted_metrics()
        broken = {"title": "x", "panels": [{"id": 1, "title": "t", "type": "stat", "targets": [{"expr": "evia_missing_total"}]}]}
        with tempfile.TemporaryDirectory() as tmp:
            dashboard = Path(tmp) / "dashboard.json"
            dashboard.write_text(json.dumps(broken), encoding="utf-8")
            with mock.patch.object(gate, "DASHBOARD_FILE", dashboard):
                failures = gate._check_dashboard(names, set())
        self.assertTrue(any("evia_missing_total" in item for item in failures))


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
            ), mock.patch.object(gate, "DASHBOARD_FILE", Path(tmp) / "missing.json"):
                with self.assertRaises(SystemExit) as caught:
                    gate.main()
        self.assertEqual(caught.exception.code, 1)


if __name__ == "__main__":
    unittest.main()
