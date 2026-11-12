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
            ), mock.patch.object(gate, "DASHBOARD_FILE", Path(tmp) / "missing.json"), mock.patch.object(
                gate, "ALERTMANAGER_FILE", Path(tmp) / "missing-am.yml"
            ):
                with self.assertRaises(SystemExit) as caught:
                    gate.main()
        self.assertEqual(caught.exception.code, 1)


class AlertmanagerGateTest(unittest.TestCase):
    def base_config(self):
        return {
            "route": {
                "receiver": "evia-warning-chat",
                "group_by": ["alertname", "job"],
                "routes": [
                    {"receiver": "evia-critical-pager", "matchers": ['severity="critical"']},
                    {"receiver": "evia-warning-chat", "matchers": ['severity="warning"']},
                ],
            },
            "inhibit_rules": [
                {"source_matchers": ['alertname="EviaScrapeTargetDown"'],
                 "target_matchers": ['severity="critical"'], "equal": ["job"]}
            ],
            "receivers": [
                {"name": "evia-critical-pager", "webhook_configs": [{"url": "http://pager.internal:5001/x"}]},
                {"name": "evia-warning-chat", "webhook_configs": [{"url": "http://chat.internal:5002/x"}]},
            ],
            "templates": ["templates/evia-notifications.tmpl"],
        }

    def write_config(self, tmp, document):
        target = Path(tmp) / "alertmanager.yml"
        target.write_text(yaml.safe_dump(document), encoding="utf-8")
        template_dir = Path(tmp) / "templates"
        template_dir.mkdir()
        (template_dir / "evia-notifications.tmpl").write_text('{{ define "x" }}{{ end }}', encoding="utf-8")
        return target

    def test_shipped_config_passes(self):
        self.assertEqual(gate._check_alertmanager({"EviaScrapeTargetDown", "EviaErasureJobFailed"}), [])

    def test_unknown_receiver_and_unreachable_receiver(self):
        with tempfile.TemporaryDirectory() as tmp:
            document = self.base_config()
            document["route"]["routes"][0]["receiver"] = "ghost"
            with mock.patch.object(gate, "ALERTMANAGER_FILE", self.write_config(tmp, document)):
                failures = gate._check_alertmanager({"EviaScrapeTargetDown"})
        self.assertTrue(any("unknown receiver" in item and "ghost" in item for item in failures))

    def test_inhibit_unknown_alert_and_bad_severity(self):
        with tempfile.TemporaryDirectory() as tmp:
            document = self.base_config()
            document["inhibit_rules"][0]["source_matchers"] = ['alertname="EviaDoesNotExist"']
            document["route"]["routes"][1]["matchers"] = ['severity="page"']
            with mock.patch.object(gate, "ALERTMANAGER_FILE", self.write_config(tmp, document)):
                failures = gate._check_alertmanager({"EviaScrapeTargetDown"})
        self.assertTrue(any("alert not defined" in item for item in failures))
        self.assertTrue(any("severity" in item for item in failures))

    def test_missing_template_and_embedded_credentials(self):
        with tempfile.TemporaryDirectory() as tmp:
            document = self.base_config()
            document["templates"] = ["templates/missing.tmpl"]
            document["receivers"][0]["webhook_configs"][0]["url"] = "http://user:secret@pager.internal:5001/x"
            with mock.patch.object(gate, "ALERTMANAGER_FILE", self.write_config(tmp, document)):
                failures = gate._check_alertmanager({"EviaScrapeTargetDown"})
        self.assertTrue(any("template" in item and "missing" in item for item in failures))
        self.assertTrue(any("embeds credentials" in item for item in failures))


if __name__ == "__main__":
    unittest.main()


class TextfileMetricsTest(unittest.TestCase):
    def test_shipped_manifest_matches_schedule_emitter(self):
        names, failures = gate.textfile_metrics()
        self.assertEqual(failures, [])
        self.assertIn("evia_backup_up", names)
        self.assertIn("evia_backup_last_success_timestamp_seconds", names)
        self.assertEqual(len(names), 6)

    def test_drift_between_manifest_and_emitter_is_caught(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            schedule = root / "schedule.py"
            schedule.write_text('METRIC_SPECS = {"evia_backup_up": ("gauge", "help")}\n')
            manifest = root / "textfile-metrics.json"
            manifest.write_text(json.dumps({"metrics": [
                {"name": "evia_backup_up", "type": "gauge", "labels": ["target"]},
                {"name": "evia_backup_ghost", "type": "gauge", "labels": ["target"]},
            ]}))
            names, failures = gate.textfile_metrics(manifest, schedule)
            self.assertIn("evia_backup_ghost", names)
            self.assertTrue(any("evia_backup_ghost" in item for item in failures))

            # An emitted spec missing from the manifest fails in the other direction.
            manifest.write_text(json.dumps({"metrics": [
                {"name": "evia_backup_up", "type": "gauge", "labels": ["target"]},
            ]}))
            _, failures = gate.textfile_metrics(manifest, schedule)
            self.assertEqual(failures, [])

            schedule.write_text('METRIC_SPECS = {"evia_backup_up": ("gauge", "h"), "evia_backup_new": ("counter", "h")}\n')
            _, failures = gate.textfile_metrics(manifest, schedule)
            self.assertTrue(any("evia_backup_new" in item for item in failures))

    def test_malformed_manifest_entries_are_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            schedule = root / "schedule.py"
            schedule.write_text('METRIC_SPECS = {"evia_backup_up": ("gauge", "h")}\n')
            manifest = root / "textfile-metrics.json"
            manifest.write_text(json.dumps({"metrics": [
                {"name": "Evia_Backup_Up", "type": "gauge", "labels": ["target"]},
                {"name": "evia_backup_up", "type": "histogram", "labels": []},
            ]}))
            _, failures = gate.textfile_metrics(manifest, schedule)
            self.assertEqual(len(failures), 2)
