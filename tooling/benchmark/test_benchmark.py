"""Pure benchmark-policy checks; these are not real dependency/load evidence."""
from datetime import timedelta
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import urllib.request

import benchmark as bench
from benchmark_workloads import event_id, WORKLOAD_NAMES, select_workloads
from benchmark_common import HTTP
from benchmark_api import Benchmark


class BenchmarkPolicies(unittest.TestCase):
    def setUp(self):
        self.config = {"contract_version": 1, "api_url": "http://127.0.0.1:8080",
                       "project_id": "00000000-0000-4000-8000-000000000001",
                       "run_id": "00000000-0000-4000-8000-000000000002", "event_key": bench.EVENT_KEY,
                       "environment": "development", "schema_version": 1, "source_kind": "server",
                       "started_at": "2026-11-08T12:15:00.123Z", "uid_cardinality": 100000}

    def result(self, rows):
        return {"rows": rows, "metadata": {"accuracy": "exact", "coverage": {"state": "provisional"}}}

    def test_nearest_rank_percentiles_and_small_samples(self):
        values = bench.distribution(range(1, 101))
        self.assertEqual((values["p50_ms"], values["p95_ms"], values["p99_ms"]), (50, 95, 99))
        self.assertFalse(values["small_sample"])
        self.assertTrue(bench.distribution([5])["small_sample"])
        self.assertEqual(bench.distribution([])["n"], 0)

    def test_only_loopback_origins(self):
        for value in ("https://evia.test", "https://127.0.0.1", "http://localhost", "http://127.0.0.1/path",
                      "http://user@localhost", "http://localhost?key=secret", "http://\n127.0.0.1", "http://[::1%eth0]"):
            with self.subTest(value=value), self.assertRaises(bench.SmokeFailure):
                bench.loopback(value)
        self.assertEqual(bench.loopback("http://[::1]:8080/"), "http://[::1]:8080")
        self.assertEqual(bench.loopback("http://127.0.0.2:8080"), "http://127.0.0.2:8080")
        self.assertEqual(bench.loopback("http://[::ffff:127.0.0.1]:8080"), "http://[::ffff:127.0.0.1]:8080")

    def test_local_http_ignores_proxy_environment_and_keeps_session_handler(self):
        with patch.dict("os.environ", {"HTTP_PROXY": "http://proxy.invalid:1234", "http_proxy": "http://proxy.invalid:1234"}):
            for session in (False, True):
                client = HTTP("http://127.0.0.1:8080", session=session)
                self.assertFalse(any(isinstance(handler, urllib.request.ProxyHandler) for handler in client.opener.handlers))
                self.assertEqual(any(isinstance(handler, urllib.request.HTTPCookieProcessor)
                                     for handler in client.opener.handlers), session)

    def test_terminal_query_failure_exposes_safe_code_only(self):
        client = object.__new__(Benchmark)
        client.project, client.query_jobs, client.timeout = self.config["project_id"], 0, 10
        import threading
        client.lock = threading.Lock()
        client.call = lambda *args, **kwargs: {"query_id": self.config["project_id"], "state": "failed",
            "error": {"code": "QUERY_COST_EXCEEDED", "message": "private diagnostics"}}
        with self.assertRaisesRegex(bench.SmokeFailure, "^QUERY_COST_EXCEEDED$"):
            client.query({"kind": "trend"})

    def test_private_config_permissions_and_version(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "config.json"
            path.write_text(json.dumps(self.config))
            path.chmod(0o600)
            self.assertEqual(bench.load_config(path)["run_id"], self.config["run_id"])
            path.chmod(0o644)
            with self.assertRaises(bench.SmokeFailure):
                bench.load_config(path)
            path.chmod(0o600)
            link = Path(directory) / "link.json"
            link.symlink_to(path)
            with self.assertRaises(bench.SmokeFailure):
                bench.load_config(link)
            self.config["contract_version"] = 2
            path.write_text(json.dumps(self.config))
            with self.assertRaises(bench.SmokeFailure):
                bench.load_config(path)

    def test_fixture_counts_include_partial_cycles(self):
        events = 100905
        for name, value in (("count", events), ("exact_uid", 100000),
                            ("high_cardinality_uid_filter", 2), ("property_filter", 10005)):
            result = self.result([{"metric_0": value}])
            self.assertEqual(bench.validate_result(name, result, self.config, events), 1)
            result["rows"][0]["metric_0"] += 1
            with self.assertRaises(bench.SmokeFailure):
                bench.validate_result(name, result, self.config, events)

    def test_section_groups_detect_missing_or_duplicate_rows(self):
        rows = [{"group_0": "section-" + str(i), "group_0_present": True,
                 "metric_0": bench.matching_modulo(103, 10, i)} for i in range(10)]
        self.assertEqual(bench.validate_result("section_group", self.result(rows), self.config, 103), 10)
        with self.assertRaises(bench.SmokeFailure):
            bench.validate_result("section_group", self.result(rows + [rows[0]]), self.config, 103)

    def test_hour_bucket_counts_exact_boundary_and_millisecond_anchor(self):
        for anchor in ("2026-11-08T12:00:00.000Z", "2026-11-08T12:15:00.123Z", "2026-11-08T12:30:00.000Z"):
            self.config["started_at"] = anchor
            start = bench.parse_time(anchor) - timedelta(seconds=1800)
            for events in (1, 2, 31, 1000):
                expected = {}
                for i in range(events):
                    moment = start + timedelta(microseconds=i * 1_800_000_000 // events)
                    key = bench.timestamp(moment.replace(minute=0, second=0, microsecond=0))
                    expected[key] = expected.get(key, 0) + 1
                rows = [{"bucket": key, "metric_0": value} for key, value in expected.items()]
                self.assertEqual(bench.validate_result("hour_bucket", self.result(rows), self.config, events), len(rows))

    def test_journey_detects_wrong_uid_duplicate_and_order(self):
        events = 300000
        rows = [{"event_id": event_id(self.config, i * 100000), "source_id": "source", "uid": bench.uid(self.config),
                 "properties": {"post_id": "post-0"}} for i in range(3)]
        self.assertEqual(bench.validate_result("journey", self.result(rows), self.config, events), 3)
        rows[1]["uid"] = "other-uid"
        with self.assertRaises(bench.SmokeFailure):
            bench.validate_result("journey", self.result(rows), self.config, events)
        rows[1]["uid"] = bench.uid(self.config)
        rows[1]["event_id"] = rows[0]["event_id"]
        with self.assertRaises(bench.SmokeFailure):
            bench.validate_result("journey", self.result(rows), self.config, events)

    def test_repeat_visit_funnel_uses_two_distinct_visits(self):
        rows = [{"step_index": 0, "subjects": 1}, {"step_index": 1, "subjects": 0}]
        bench.validate_result("ordered_funnel", self.result(rows), self.config, 100000)
        with self.assertRaises(bench.SmokeFailure):
            bench.validate_result("ordered_funnel", self.result(rows), self.config, 100001)
        rows[1]["subjects"] = 1
        bench.validate_result("ordered_funnel", self.result(rows), self.config, 100001)

    def test_workloads_pin_versions_and_bound_funnel_input(self):
        cases = bench.workloads(self.config)
        self.assertEqual(len(cases), 8)
        self.assertNotIn("event_versions", cases["ordered_funnel"])
        self.assertEqual(cases["ordered_funnel"]["identity_filter"], {"uid": bench.uid(self.config)})
        self.assertEqual(cases["ordered_funnel"]["funnel"]["window_seconds"], 1800)
        self.assertEqual(cases["journey"]["kind"], "journey")
        self.assertEqual(cases["exact_uid"]["metrics"], [{"op": "unique", "accuracy": "exact"}])

    def test_default_workload_selection_preserves_complete_order(self):
        self.assertEqual(select_workloads(), ["count", "exact_uid", "hour_bucket", "section_group",
                                             "high_cardinality_uid_filter", "property_filter", "journey", "ordered_funnel"])
        self.assertEqual(tuple(bench.workloads(self.config)), WORKLOAD_NAMES)

    def test_partial_workload_selection_preserves_requested_order(self):
        self.assertEqual(select_workloads("hour_bucket,count,exact_uid"), ["hour_bucket", "count", "exact_uid"])
        self.assertEqual(select_workloads(" count , exact_uid , hour_bucket "), ["count", "exact_uid", "hour_bucket"])

    def test_invalid_workload_selections_fail_before_api_side_effects(self):
        for value in ("", " ", ",", "count,", ",count", "count,,exact_uid", "unknown",
                      "count,unknown", "count,count", "count, count", "Count"):
            with self.subTest(value=value), patch("sys.argv", ["benchmark.py", "queries", "--config", "/tmp/unused.json",
                      "--events", "1000", "--report", "/tmp/unused-report.json", "--workloads", value]), \
                    patch.object(bench, "credentials") as credentials, patch.object(bench, "Benchmark") as api:
                with self.assertRaises(bench.SmokeFailure):
                    bench.main()
                credentials.assert_not_called()
                api.assert_not_called()

    def test_partial_matrix_records_selection_and_keeps_visibility_and_cancel(self):
        observed = []

        def query(request, cancel=False):
            observed.append((request["metrics"][0]["op"], cancel))
            if cancel:
                return {"state": "canceled", "latency_ms": 1, "submit_ms": 1}
            return {"result": self.result([{"metric_0": 1000}]), "latency_ms": 1, "submit_ms": 1}

        client = SimpleNamespace(query=query, query_jobs=5, http=SimpleNamespace(statuses={}))
        args = SimpleNamespace(action="queries", events=1000, drain_timeout=1, ingestion_report=None,
                               samples=1, concurrency=[1], workloads="count")
        report = bench.run_queries(client, self.config, args)
        self.assertEqual(report["selected_workloads"], ["count"])
        self.assertEqual([(group["workload"], group["phase"]) for group in report["query_groups"]],
                         [("count", "first_observed"), ("count", "warm")])
        self.assertEqual(observed, [("count", False), ("unique", False), ("count", False),
                                    ("count", False), ("count", True)])
        self.assertTrue(report["visibility"]["exact_count_verified"])
        self.assertTrue(report["visibility"]["exact_uid_verified"])
        self.assertEqual(report["cancellation"]["state"], "canceled")

    def test_journey_snapshot_guard_only_applies_when_selected(self):
        config = {**self.config, "uid_cardinality": 1}
        with tempfile.TemporaryDirectory() as directory:
            for selection in ("count", "ordered_funnel", "journey", None):
                arguments = ["benchmark.py", "queries", "--config", "/tmp/unused.json", "--events", "1000",
                             "--report", str(Path(directory) / "report.json")]
                if selection is not None:
                    arguments += ["--workloads", selection]
                with self.subTest(selection=selection), patch("sys.argv", arguments), \
                        patch.object(bench, "credentials", return_value={}), \
                        patch.object(bench, "load_config", return_value=config), \
                        patch.object(bench, "Benchmark") as api, \
                        patch.object(bench, "run_queries", return_value={"success": True}) as run:
                    api.return_value.query_jobs = 0
                    if selection in ("count", "ordered_funnel"):
                        self.assertEqual(bench.main(), 0)
                        api.assert_called_once()
                        run.assert_called_once()
                    else:
                        with self.assertRaisesRegex(bench.SmokeFailure, "Known UID journey exceeds snapshot budget"):
                            bench.main()
                        api.assert_not_called()
                        run.assert_not_called()

    def test_drain_retains_existing_journey_snapshot_guard(self):
        arguments = ["benchmark.py", "drain", "--config", "/tmp/unused.json", "--events", "1000",
                     "--report", "/tmp/unused-report.json"]
        with patch("sys.argv", arguments), patch.object(bench, "credentials", return_value={}), \
                patch.object(bench, "load_config", return_value={**self.config, "uid_cardinality": 1}), \
                patch.object(bench, "Benchmark") as api:
            with self.assertRaisesRegex(bench.SmokeFailure, "Known UID journey exceeds snapshot budget"):
                bench.main()
            api.assert_not_called()


if __name__ == "__main__":
    unittest.main()
