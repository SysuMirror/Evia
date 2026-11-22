"""Unit tests for pure replay-capacity reconciliation and CLI safety."""
import copy
import json
import os
from pathlib import Path
import stat
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))

from benchmark_common import SmokeFailure
import session_replay_capacity as replay


DIGEST = "a" * 64
FROZEN_AT = "2026-11-22T12:00:00Z"


def scenario(profile, budget):
    worker = {
        "pending_after_recovery": 0,
        "duplicate_chunks_after_recovery": 0,
        "transition_ms": 1_000,
        "generation_before_transition": "worker-generation-1",
        "generation_after_recovery": (
            "worker-generation-1" if profile == "baseline" else "worker-generation-2"
        ),
        "restart_count_before_transition": 0,
        "restart_count_after_recovery": 0 if profile == "baseline" else 1,
    }
    if profile == "baseline":
        worker.update({
            "pending_before_drain": 12,
            "graceful_drain_completed": True,
            "restart_count": 0,
        })
    else:
        worker.update({
            "pending_before_restart": 12,
            "restart_count": 1,
            "restart_recovery_completed": True,
        })
    principal = {
        "status_code": 200,
        "pages": 2,
        "items": 9,
        "distinct_items": 9,
        "duplicate_items": 0,
        "missing_items": 0,
        "cursor_errors": 0,
        "item_set_sha256": DIGEST,
        "first_page_frozen_at": FROZEN_AT,
        "as_of_values": [FROZEN_AT],
    }
    latency = {"samples": 30, "p95_ms": 100, "max_ms": 150}
    return {
        "name": f"{profile}-{budget}",
        "profile": profile,
        "budget_encoded_chars": budget,
        "browser": {
            "accepted_chunks": 100,
            "accepted_items": 10,
            "receipt_rejections": 0,
            "unknown_outcomes": 0,
            "collect_status_counts": {"202": 5},
            "accepted_encoded_chars_total": budget * 3,
            "accepted_gzip_bytes_total": budget * 2,
            "max_session_encoded_chars": budget - 10,
            "largest_attempted_rejected_payload_encoded_chars": 20,
            "budget_checkpoint": {
                "accepted_encoded_chars": budget - 10,
                "reserved_encoded_chars": 0,
                "pending_encoded_chars": 0,
            },
            "budget_boundary_reached_sessions": 3,
            "budget_overshoot_sessions": 0,
        },
        "capture": {
            "wall_time_ms": 600_000,
            "pagehide_triggered": True,
            "pagehide_finalized": True,
            "pagehide_finalized_before_context_close": True,
        },
        "clickhouse": {
            "distinct_chunks": 100,
            "distinct_items": 10,
            "raw_items": 11,
            "incomplete_raw_items": 1,
            "plaintext_content_rows": 0,
            "disk_reserve_bytes_before": 3 * 1024 * replay.MIB,
            "disk_reserve_bytes_after": 2 * 1024 * replay.MIB,
        },
        "postgres": {
            "chunks": 100,
            "items": 10,
            "raw_items": 11,
            "incomplete_raw_items": 1,
            "artifacts": 100,
        },
        "privacy": {
            "canary_count": 2,
            "canary_injection": {
                "password_input_injected": True,
                "masked_region_injected": True,
                "injected_before_capture": True,
            },
            "canary_matches": {
                "browser_request_properties": 0,
                "clickhouse_properties": 0,
                "artifact_plaintext": 0,
            },
        },
        "worker": worker,
        "pagination": {
            "frozen_at": FROZEN_AT,
            "shared_first_page_freeze": True,
            "appended_after_freeze_items": 1,
            "frozen_expected_items": 9,
            "discovery_excluded_incomplete_items": 1,
            "owner": {**principal, "auth_kind": "owner_session"},
            "machine": {**principal, "auth_kind": "machine_credential"},
        },
        "latency": {
            "collect_ack": dict(latency),
            "artifact_ready": dict(latency),
            "owner_page": dict(latency),
            "machine_page": dict(latency),
        },
        "resources": {
            "api_cpu_millicores": 2_000 if profile == "baseline" else 1_000,
            "api_memory_bytes": 2 * 1024 * replay.MIB if profile == "baseline" else 1024 * replay.MIB,
            "content_worker_cpu_millicores": 2_000 if profile == "baseline" else 500,
            "content_worker_memory_bytes": 2 * 1024 * replay.MIB if profile == "baseline" else 1024 * replay.MIB,
        },
        "rss": {
            "samples": 30,
            "api_peak_bytes": 256 * replay.MIB,
            "content_worker_peak_bytes": 512 * replay.MIB,
        },
    }


def report():
    return {
        "report_version": replay.REPORT_VERSION,
        "kind": replay.REPORT_KIND,
        "synthetic": True,
        "production_writes": False,
        "environment": "development",
        "api_url": "http://127.0.0.1:8081",
        "namespace": "evia_bench_012345abcdef",
        "scenarios": [scenario(profile, budget)
                      for profile, budget in replay.EXPECTED_SCENARIOS],
    }


class ReportEvaluationTest(unittest.TestCase):
    def test_complete_matrix_passes_and_preserves_volume_units(self):
        result = replay.evaluate_report(report())
        self.assertTrue(result["passed"], result)
        self.assertEqual(result["budget_unit"], "encoded_base64url_characters")
        self.assertEqual(result["gzip_volume_unit"], "bytes")
        self.assertEqual(
            {(case["profile"], case["budget_encoded_chars"]) for case in result["scenarios"]},
            set(replay.EXPECTED_SCENARIOS),
        )
        self.assertEqual(
            result["scenarios"][0]["volume_units"]["accepted_gzip_bytes_total"],
            2 * replay.ENCODED_CHARACTER_BUDGETS[0],
        )

    def test_missing_and_duplicate_matrix_cases_fail(self):
        value = report()
        value["scenarios"].pop()
        self.assertFalse(replay.evaluate_report(value)["passed"])
        value = report()
        value["scenarios"][-1] = copy.deepcopy(value["scenarios"][0])
        result = replay.evaluate_report(value)
        self.assertFalse(result["passed"])
        self.assertTrue(any("duplicate" in failure for failure in result["global_failures"]))

    def test_report_boundary_fails_closed(self):
        for key, invalid in (
            ("synthetic", False),
            ("production_writes", True),
            ("api_url", "https://evia.example"),
            ("namespace", "production"),
        ):
            with self.subTest(key=key):
                value = report()
                value[key] = invalid
                self.assertFalse(replay.evaluate_report(value)["passed"])

    def test_chunk_item_and_artifact_mismatches_fail(self):
        mutations = (
            ("clickhouse", "distinct_chunks", 99),
            ("postgres", "chunks", 99),
            ("clickhouse", "distinct_items", 9),
            ("postgres", "items", 9),
            ("postgres", "artifacts", 9),
        )
        for section, key, value in mutations:
            with self.subTest(section=section, key=key):
                candidate = report()
                candidate["scenarios"][0][section][key] = value
                result = replay.evaluate_report(candidate)
                self.assertFalse(result["passed"])
                self.assertFalse(result["scenarios"][0]["passed"])

    def test_encoded_character_budget_not_gzip_bytes_drives_boundary(self):
        value = report()
        case = value["scenarios"][0]
        # Gzip bytes are reported independently; encoded characters alone drive
        # the configured per-session quota gate.
        case["browser"]["accepted_gzip_bytes_total"] = 99 * replay.MIB
        self.assertTrue(replay.evaluate_report(value)["passed"])
        case["browser"]["max_session_encoded_chars"] = case["budget_encoded_chars"] + 1
        result = replay.evaluate_report(value)
        self.assertFalse(result["passed"])
        self.assertTrue(any("exceeded budget_encoded_chars" in failure
                            for failure in result["scenarios"][0]["failures"]))

    def test_unexercised_budget_boundary_and_overshoot_fail(self):
        value = report()
        case = value["scenarios"][0]
        case["browser"]["max_session_encoded_chars"] = case["budget_encoded_chars"] - 100
        case["browser"]["budget_checkpoint"]["accepted_encoded_chars"] = (
            case["budget_encoded_chars"] - 100
        )
        case["browser"]["largest_attempted_rejected_payload_encoded_chars"] = 50
        case["browser"]["budget_overshoot_sessions"] = 1
        result = replay.evaluate_report(value)
        self.assertFalse(result["passed"])
        failures = result["scenarios"][0]["failures"]
        self.assertTrue(any("boundary was not exercised" in failure for failure in failures))
        self.assertTrue(any("overshoot" in failure for failure in failures))

    def test_budget_checkpoint_rejected_payload_and_pagehide_fail_closed(self):
        value = report()
        case = value["scenarios"][0]
        case["browser"]["budget_checkpoint"]["reserved_encoded_chars"] = 1
        case["browser"]["largest_attempted_rejected_payload_encoded_chars"] = 1
        case["capture"]["pagehide_finalized_before_context_close"] = False
        result = replay.evaluate_report(value)
        self.assertFalse(result["passed"])
        failures = result["scenarios"][0]["failures"]
        self.assertTrue(any("reserved and pending" in failure for failure in failures))
        self.assertTrue(any("largest attempted rejected" in failure for failure in failures))
        self.assertTrue(any("before browser context close" in failure for failure in failures))

    def test_capture_wall_time_is_report_only_not_collect_ack_latency(self):
        value = report()
        value["scenarios"][0]["capture"]["wall_time_ms"] = 24 * 60 * 60 * 1000
        result = replay.evaluate_report(value)
        self.assertTrue(result["passed"], result)
        self.assertTrue(result["scenarios"][0]["capture"]["report_only"])

    def test_raw_incomplete_discovery_and_clickhouse_disk_gates_fail(self):
        value = report()
        case = value["scenarios"][0]
        case["pagination"]["discovery_excluded_incomplete_items"] = 0
        case["clickhouse"]["disk_reserve_bytes_after"] = (
            replay.MIN_CLICKHOUSE_DISK_RESERVE_BYTES - 1
        )
        result = replay.evaluate_report(value)
        self.assertFalse(result["passed"])
        failures = result["scenarios"][0]["failures"]
        self.assertTrue(any("exclude every incomplete" in failure for failure in failures))
        self.assertTrue(any("2 GiB" in failure for failure in failures))

    def test_privacy_canary_and_plaintext_fail(self):
        value = report()
        case = value["scenarios"][0]
        case["privacy"]["canary_matches"]["artifact_plaintext"] = 1
        case["privacy"]["canary_injection"]["masked_region_injected"] = False
        case["clickhouse"]["plaintext_content_rows"] = 1
        result = replay.evaluate_report(value)
        self.assertFalse(result["passed"])
        failures = result["scenarios"][0]["failures"]
        self.assertTrue(any("artifact_plaintext" in failure for failure in failures))
        self.assertTrue(any("plaintext_content_rows" in failure for failure in failures))

    def test_weak_resources_must_be_lower_than_baseline(self):
        value = report()
        for case in value["scenarios"]:
            if case["profile"] == "weak":
                matching = next(candidate for candidate in value["scenarios"]
                                if candidate["profile"] == "baseline"
                                and candidate["budget_encoded_chars"] == case["budget_encoded_chars"])
                case["resources"] = copy.deepcopy(matching["resources"])
        result = replay.evaluate_report(value)
        self.assertFalse(result["passed"])
        self.assertTrue(any("resources must be no larger" in failure
                            for failure in result["global_failures"]))

    def test_baseline_drain_and_weak_restart_are_profile_specific(self):
        value = report()
        value["scenarios"][0]["worker"]["graceful_drain_completed"] = False
        value["scenarios"][0]["worker"]["generation_after_recovery"] = "unexpected"
        weak = next(case for case in value["scenarios"] if case["profile"] == "weak")
        weak["worker"]["restart_count"] = 0
        weak["worker"]["restart_count_after_recovery"] = 0
        result = replay.evaluate_report(value)
        self.assertFalse(result["passed"])
        baseline_result = result["scenarios"][0]
        weak_result = next(case for case in result["scenarios"] if case["profile"] == "weak")
        self.assertTrue(any("graceful drain" in failure for failure in baseline_result["failures"]))
        self.assertTrue(any("restart" in failure for failure in weak_result["failures"]))

    def test_frozen_pagination_and_owner_machine_parity_fail(self):
        value = report()
        case = value["scenarios"][0]
        case["pagination"]["owner"]["as_of_values"] = [FROZEN_AT, "2026-11-22T12:01:00Z"]
        case["pagination"]["machine"]["first_page_frozen_at"] = "2026-11-22T12:01:00Z"
        case["pagination"]["shared_first_page_freeze"] = False
        case["pagination"]["machine"]["item_set_sha256"] = "b" * 64
        result = replay.evaluate_report(value)
        self.assertFalse(result["passed"])
        failures = result["scenarios"][0]["failures"]
        self.assertTrue(any("as_of_values" in failure for failure in failures))
        self.assertTrue(any("shared freeze" in failure for failure in failures))
        self.assertTrue(any("identical" in failure for failure in failures))

    def test_latency_rss_and_sample_gates_fail(self):
        value = report()
        case = value["scenarios"][0]
        limits = replay.PROFILE_LIMITS["baseline"]
        case["latency"]["collect_ack"]["p95_ms"] = limits["collect_ack_p95_ms"] + 1
        case["latency"]["collect_ack"]["max_ms"] = limits["collect_ack_p95_ms"] + 2
        case["latency"]["owner_page"]["samples"] = 29
        case["rss"]["samples"] = 29
        case["rss"]["api_peak_bytes"] = limits["api_peak_rss_bytes"] + 1
        result = replay.evaluate_report(value)
        self.assertFalse(result["passed"])
        failures = result["scenarios"][0]["failures"]
        self.assertTrue(any("collect_ack.p95_ms exceeded" in failure for failure in failures))
        self.assertTrue(any("owner_page.samples" in failure for failure in failures))
        self.assertTrue(any("rss.samples" in failure for failure in failures))
        self.assertTrue(any("api_peak_bytes exceeded" in failure for failure in failures))


class CliSafetyTest(unittest.TestCase):
    def _private_report(self, directory):
        path = Path(directory) / "input.json"
        path.write_text(json.dumps(report()), encoding="utf-8")
        path.chmod(0o600)
        return path

    def test_cli_writes_new_mode_0600_output(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            source = self._private_report(directory)
            output = Path(directory) / "evaluation.json"
            status = replay.main([
                "--api-url", "http://127.0.0.1:8081",
                "--namespace", "evia_bench_012345abcdef",
                "--input", str(source),
                "--out", str(output),
            ])
            self.assertEqual(status, 0)
            self.assertTrue(json.loads(output.read_text(encoding="utf-8"))["passed"])
            self.assertEqual(stat.S_IMODE(output.stat().st_mode), 0o600)

    def test_cli_rejects_non_loopback_before_read_or_write(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            output = Path(directory) / "evaluation.json"
            with self.assertRaises(SmokeFailure):
                replay.main([
                    "--api-url", "https://production.example",
                    "--namespace", "evia_bench_012345abcdef",
                    "--input", str(Path(directory) / "missing.json"),
                    "--out", str(output),
                ])
            self.assertFalse(output.exists())

    def test_cli_rejects_namespace_mismatch_and_existing_output(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            source = self._private_report(directory)
            output = Path(directory) / "evaluation.json"
            with self.assertRaises(SmokeFailure):
                replay.main([
                    "--api-url", "http://127.0.0.1:8081",
                    "--namespace", "evia_bench_aaaaaaaaaaaa",
                    "--input", str(source),
                    "--out", str(output),
                ])
            output.write_text("do not replace", encoding="utf-8")
            output.chmod(0o600)
            with self.assertRaises(SmokeFailure):
                replay.main([
                    "--api-url", "http://127.0.0.1:8081",
                    "--namespace", "evia_bench_012345abcdef",
                    "--input", str(source),
                    "--out", str(output),
                ])
            self.assertEqual(output.read_text(encoding="utf-8"), "do not replace")

    def test_cli_requires_private_input_under_tmp(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            source = Path(directory) / "input.json"
            source.write_text(json.dumps(report()), encoding="utf-8")
            source.chmod(0o644)
            with self.assertRaises(SmokeFailure):
                replay.main([
                    "--api-url", "http://127.0.0.1:8081",
                    "--namespace", "evia_bench_012345abcdef",
                    "--input", str(source),
                    "--out", str(Path(directory) / "evaluation.json"),
                ])


if __name__ == "__main__":
    unittest.main()
