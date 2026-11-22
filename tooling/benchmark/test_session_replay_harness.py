"""Unit tests for the executable session-replay capacity harness."""
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
import session_replay_capacity as capacity
import session_replay_harness as harness
from test_session_replay_capacity import scenario


EXECUTABLE = str(Path(sys.executable).resolve())


def settings_value(report_dir):
    command = [EXECUTABLE, "-c", "print('{}')"]
    return {
        "harness_version": harness.HARNESS_VERSION,
        "kind": harness.SETTINGS_KIND,
        "synthetic": True,
        "production_writes": False,
        "environment": "development",
        "api_url": "http://127.0.0.1:8081",
        "fixture_url": "http://[::1]:4174",
        "fixture_kind": harness.FIXTURE_KIND,
        "new_sse_mode": harness.NEW_SSE_MODE,
        "namespace": "evia_bench_012345abcdef",
        "report_dir": str(report_dir),
        "hook_timeout_seconds": 3_600,
        "resources": {
            "baseline": {
                "api_cpu_millicores": 2_000,
                "api_memory_bytes": 2 * 1024 * capacity.MIB,
                "content_worker_cpu_millicores": 2_000,
                "content_worker_memory_bytes": 2 * 1024 * capacity.MIB,
            },
            "weak": {
                "api_cpu_millicores": 1_000,
                "api_memory_bytes": 1024 * capacity.MIB,
                "content_worker_cpu_millicores": 500,
                "content_worker_memory_bytes": 1024 * capacity.MIB,
            },
        },
        "commands": {name: list(command) for name in harness.HOOK_NAMES},
    }


def write_settings(directory, mutate=None):
    report_dir = Path(directory) / "cells"
    report_dir.mkdir(mode=0o700)
    value = settings_value(report_dir)
    if mutate:
        mutate(value)
    path = Path(directory) / "settings.json"
    path.write_text(json.dumps(value), encoding="utf-8")
    path.chmod(0o600)
    return path, report_dir, value


def prepare_ack(settings, profile, budget):
    return {
        "fixture_kind": harness.FIXTURE_KIND,
        "new_sse_mode": harness.NEW_SSE_MODE,
        "api_url": settings["api_url"],
        "fixture_url": settings["fixture_url"],
        "namespace": settings["namespace"],
        "cell_id": harness.cell_id(profile, budget),
        "environment": "development",
        "synthetic": True,
        "production_writes": False,
        "dedicated_namespace": True,
        "ordinary_development_mode": False,
        "fresh_project": True,
        "project_fixture_id": "fixture-" + harness.cell_id(profile, budget),
    }


def hook_result(settings, profile, budget, hook):
    complete = scenario(profile, budget)
    if hook == "prepare_cell":
        return prepare_ack(settings, profile, budget)
    if hook == "worker_hold":
        return {"hold_armed": True}
    if hook == "browser_workload":
        return {
            "checkpoint": {
                "accepted_items_before_freeze": complete["browser"]["accepted_items"] - 1,
                "accepted_chunks_before_transition": complete["browser"]["accepted_chunks"] - 1,
            },
            "browser": {
                "budget_checkpoint": complete["browser"]["budget_checkpoint"],
                "largest_attempted_rejected_payload_encoded_chars": complete["browser"][
                    "largest_attempted_rejected_payload_encoded_chars"
                ],
            },
            "capture": complete["capture"],
            "privacy": {
                "canary_injection": complete["privacy"]["canary_injection"],
            },
        }
    if hook in ("worker_drain", "worker_restart"):
        return {"worker": complete["worker"]}
    if hook == "frozen_pagination":
        return {
            "browser": {
                key: value for key, value in complete["browser"].items()
                if key not in {
                    "budget_checkpoint",
                    "largest_attempted_rejected_payload_encoded_chars",
                }
            },
            "pagination": complete["pagination"],
            "latency": {
                "collect_ack": complete["latency"]["collect_ack"],
                "owner_page": complete["latency"]["owner_page"],
                "machine_page": complete["latency"]["machine_page"],
            },
            "privacy": {
                "canary_count": complete["privacy"]["canary_count"],
                "canary_matches": {
                    "browser_request_properties": 0,
                },
            },
        }
    if hook == "reconcile":
        return {
            "clickhouse": complete["clickhouse"],
            "postgres": complete["postgres"],
            "latency": {
                "artifact_ready": complete["latency"]["artifact_ready"],
            },
            "privacy": {
                "canary_matches": {
                    "clickhouse_properties": 0,
                    "artifact_plaintext": 0,
                },
            },
            "rss": complete["rss"],
        }
    raise AssertionError(hook)


class SettingsTest(unittest.TestCase):
    def test_private_settings_validate_all_commands_and_fixture_boundary(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            source, report_dir, _ = write_settings(directory)
            loaded = harness.load_settings(source)
            self.assertEqual(loaded["namespace"], "evia_bench_012345abcdef")
            self.assertEqual(loaded["report_dir"], report_dir)
            self.assertEqual(set(loaded["commands"]), set(harness.HOOK_NAMES))

    def test_refuses_ordinary_new_sse_mode_and_non_loopback_endpoints(self):
        mutations = (
            lambda value: value.update(new_sse_mode="development"),
            lambda value: value.update(api_url="https://production.example"),
            lambda value: value.update(fixture_url="http://fixture.internal:4174"),
        )
        for index, mutate in enumerate(mutations):
            with self.subTest(index=index), tempfile.TemporaryDirectory(dir="/tmp") as directory:
                source, _, _ = write_settings(directory, mutate)
                with self.assertRaises((SmokeFailure, TypeError)):
                    harness.load_settings(source)

    def test_refuses_missing_dedicated_namespace_and_non_private_settings(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            source, _, _ = write_settings(
                directory, lambda value: value.update(namespace="development")
            )
            with self.assertRaises(SmokeFailure):
                harness.load_settings(source)
            source.chmod(0o644)
            with self.assertRaises(SmokeFailure):
                harness.load_settings(source)

    def test_refuses_incomplete_commands_and_unsafe_report_directory(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            source, report_dir, value = write_settings(directory)
            del value["commands"]["worker_restart"]
            source.write_text(json.dumps(value), encoding="utf-8")
            source.chmod(0o600)
            with self.assertRaises(SmokeFailure):
                harness.load_settings(source)
            value = settings_value(report_dir)
            value["commands"]["worker_restart"][0] = "python3"
            source.write_text(json.dumps(value), encoding="utf-8")
            source.chmod(0o600)
            with self.assertRaises(SmokeFailure):
                harness.load_settings(source)
            value = settings_value(report_dir)
            source.write_text(json.dumps(value), encoding="utf-8")
            source.chmod(0o600)
            report_dir.chmod(0o755)
            with self.assertRaises(SmokeFailure):
                harness.load_settings(source)

    def test_refuses_weak_resources_that_are_not_reduced(self):
        def mutate(value):
            value["resources"]["weak"] = copy.deepcopy(value["resources"]["baseline"])
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            source, _, _ = write_settings(directory, mutate)
            with self.assertRaises(SmokeFailure):
                harness.load_settings(source)

    def test_fixture_values_are_deterministic_and_cell_specific(self):
        first = harness.fixture_values("evia_bench_012345abcdef", "baseline", 2 * capacity.MIB)
        again = harness.fixture_values("evia_bench_012345abcdef", "baseline", 2 * capacity.MIB)
        weak = harness.fixture_values("evia_bench_012345abcdef", "weak", 2 * capacity.MIB)
        self.assertEqual(first, again)
        self.assertNotEqual(first, weak)
        self.assertNotEqual(first["password_canary"], first["masked_canary"])


class OrchestrationTest(unittest.TestCase):
    def _loaded(self, directory):
        source, _, _ = write_settings(directory)
        return harness.load_settings(source)

    def test_baseline_uses_drain_and_weak_uses_restart_in_fixed_order(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            settings = self._loaded(directory)
            for profile, transition in (("baseline", "worker_drain"), ("weak", "worker_restart")):
                calls = []
                def runner(current, observed_profile, budget, hook):
                    calls.append(hook)
                    return hook_result(current, observed_profile, budget, hook)
                observed = harness.execute_cell(
                    settings, profile, 2 * capacity.MIB, hook_runner=runner
                )
                self.assertEqual(calls, [
                    "prepare_cell", "worker_hold", "browser_workload", transition,
                    "frozen_pagination", "reconcile",
                ])
                self.assertTrue(capacity.reconcile_scenario(observed)["passed"])

    def test_prepare_attestation_refuses_ordinary_mode_before_workload(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            settings = self._loaded(directory)
            calls = []
            def runner(current, profile, budget, hook):
                calls.append(hook)
                value = prepare_ack(current, profile, budget)
                value["ordinary_development_mode"] = True
                return value
            with self.assertRaises(SmokeFailure):
                harness.execute_cell(
                    settings, "baseline", 2 * capacity.MIB, hook_runner=runner
                )
            self.assertEqual(calls, ["prepare_cell"])

    def test_browser_pagehide_and_split_merge_contract_fail_closed(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            settings = self._loaded(directory)
            calls = []
            def unfinished(current, profile, budget, hook):
                calls.append(hook)
                value = hook_result(current, profile, budget, hook)
                if hook == "browser_workload":
                    value["capture"]["pagehide_finalized_before_context_close"] = False
                return value
            with self.assertRaisesRegex(SmokeFailure, "pagehide"):
                harness.execute_cell(
                    settings, "baseline", 2 * capacity.MIB, hook_runner=unfinished
                )
            self.assertEqual(calls, ["prepare_cell", "worker_hold", "browser_workload"])

            def duplicate(current, profile, budget, hook):
                value = hook_result(current, profile, budget, hook)
                if hook == "frozen_pagination":
                    value["browser"]["budget_checkpoint"] = {
                        "accepted_encoded_chars": budget,
                        "reserved_encoded_chars": 0,
                        "pending_encoded_chars": 0,
                    }
                return value
            with self.assertRaisesRegex(SmokeFailure, "budget_checkpoint"):
                harness.execute_cell(
                    settings, "baseline", 2 * capacity.MIB, hook_runner=duplicate
                )

    def test_cumulative_browser_counts_cannot_regress(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            settings = self._loaded(directory)
            def runner(current, profile, budget, hook):
                value = hook_result(current, profile, budget, hook)
                if hook == "frozen_pagination":
                    value["browser"]["accepted_items"] = 1
                return value
            with self.assertRaises(SmokeFailure):
                harness.execute_cell(
                    settings, "baseline", 2 * capacity.MIB, hook_runner=runner
                )

    def test_hook_receives_fixed_weak_network_and_scrubbed_environment(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            settings = self._loaded(directory)
            marker = Path(directory) / "hook.py"
            marker.write_text(
                "import json,os; print(json.dumps({"
                "'network':json.loads(os.environ['EVIA_REPLAY_NETWORK_JSON']),"
                "'profile':os.environ['EVIA_REPLAY_PROFILE'],"
                "'ambient':os.environ.get('EVIA_E2E_PASSWORD')}))",
                encoding="utf-8",
            )
            for name in harness.HOOK_NAMES:
                settings["commands"][name] = (EXECUTABLE, str(marker))
            previous = os.environ.get("EVIA_E2E_PASSWORD")
            os.environ["EVIA_E2E_PASSWORD"] = "must-not-leak"
            try:
                result = harness.run_hook(
                    settings, "weak", 8 * capacity.MIB, "browser_workload"
                )
            finally:
                if previous is None:
                    os.environ.pop("EVIA_E2E_PASSWORD", None)
                else:
                    os.environ["EVIA_E2E_PASSWORD"] = previous
            self.assertEqual(result["profile"], "weak")
            self.assertIsNone(result["ambient"])
            self.assertEqual(result["network"], harness.NETWORK_PROFILES["weak"])


class ResumeAndOutputTest(unittest.TestCase):
    def test_dry_run_validates_without_running_or_writing(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            source, report_dir, _ = write_settings(directory)
            output = Path(directory) / "summary.json"
            status = harness.main([
                "--settings", str(source), "--out", str(output), "--dry-run",
            ])
            self.assertEqual(status, 0)
            self.assertFalse(output.exists())
            self.assertEqual(list(report_dir.iterdir()), [])

    def test_run_writes_private_cells_and_evaluator_compatible_summary(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            source, report_dir, _ = write_settings(directory)
            settings = harness.load_settings(source)
            output = Path(directory) / "summary.json"
            evaluation, resumed = harness.run(settings, output, hook_runner=hook_result)
            self.assertTrue(evaluation["passed"], evaluation)
            self.assertEqual(resumed, 0)
            self.assertEqual(stat.S_IMODE(output.stat().st_mode), 0o600)
            report = json.loads(output.read_text(encoding="utf-8"))
            self.assertTrue(capacity.evaluate_report(report)["passed"])
            cells = sorted(report_dir.iterdir())
            self.assertEqual(len(cells), 4)
            self.assertTrue(all(stat.S_IMODE(path.stat().st_mode) == 0o600 for path in cells))

    def test_assemble_manual_fragments_writes_complete_report(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            source, _, _ = write_settings(directory)
            settings = harness.load_settings(source)
            fragments = Path(directory) / "fragments"
            fragments.mkdir(mode=0o700)
            for profile, budget in capacity.EXPECTED_SCENARIOS:
                path = fragments / (harness.cell_id(profile, budget) + ".json")
                value = scenario(profile, budget)
                value["network"] = harness.NETWORK_PROFILES[profile]
                path.write_text(json.dumps(value), encoding="utf-8")
                path.chmod(0o600)
            output = Path(directory) / "assembled.json"
            evaluation = harness.assemble_fragments(settings, fragments, output)
            self.assertTrue(evaluation["passed"], evaluation)
            assembled = json.loads(output.read_text(encoding="utf-8"))
            self.assertEqual(assembled["report_version"], capacity.REPORT_VERSION)
            self.assertEqual(len(assembled["scenarios"]), 4)

    def test_assemble_fragments_reports_missing_cell_and_evidence(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            source, _, _ = write_settings(directory)
            settings = harness.load_settings(source)
            fragments = Path(directory) / "fragments"
            fragments.mkdir(mode=0o700)
            for profile, budget in capacity.EXPECTED_SCENARIOS[:-1]:
                value = scenario(profile, budget)
                value["network"] = harness.NETWORK_PROFILES[profile]
                if (profile, budget) == capacity.EXPECTED_SCENARIOS[0]:
                    del value["capture"]["pagehide_finalized_before_context_close"]
                path = fragments / (harness.cell_id(profile, budget) + ".json")
                path.write_text(json.dumps(value), encoding="utf-8")
                path.chmod(0o600)
            with self.assertRaisesRegex(SmokeFailure, "Missing manual replay cell fragments"):
                harness.assemble_fragments(
                    settings, fragments, Path(directory) / "missing-cell.json"
                )
            profile, budget = capacity.EXPECTED_SCENARIOS[-1]
            path = fragments / (harness.cell_id(profile, budget) + ".json")
            value = scenario(profile, budget)
            value["network"] = harness.NETWORK_PROFILES[profile]
            path.write_text(json.dumps(value), encoding="utf-8")
            path.chmod(0o600)
            evaluation = harness.assemble_fragments(
                settings, fragments, Path(directory) / "missing-evidence.json"
            )
            self.assertFalse(evaluation["passed"])
            self.assertTrue(any(
                "pagehide_finalized_before_context_close" in failure
                for failure in harness.evaluation_failures(evaluation)
            ))

    def test_resume_reads_completed_cells_without_executing_hooks(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            source, _, _ = write_settings(directory)
            settings = harness.load_settings(source)
            first_output = Path(directory) / "first.json"
            harness.run(settings, first_output, hook_runner=hook_result)
            second_output = Path(directory) / "second.json"
            def forbidden(*_args):
                raise AssertionError("completed cells must not execute")
            evaluation, resumed = harness.run(settings, second_output, hook_runner=forbidden)
            self.assertTrue(evaluation["passed"])
            self.assertEqual(resumed, 4)

    def test_existing_summary_is_refused_before_any_hook(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            source, _, _ = write_settings(directory)
            output = Path(directory) / "summary.json"
            output.write_text("do not replace", encoding="utf-8")
            output.chmod(0o600)
            with self.assertRaises(SmokeFailure):
                harness.main(["--settings", str(source), "--out", str(output)])
            self.assertEqual(output.read_text(encoding="utf-8"), "do not replace")

    def test_unexpected_existing_report_file_is_refused(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            source, report_dir, _ = write_settings(directory)
            unexpected = report_dir / "old-output.json"
            unexpected.write_text("{}", encoding="utf-8")
            unexpected.chmod(0o600)
            settings = harness.load_settings(source)
            with self.assertRaises(SmokeFailure):
                harness.plan(settings)

    def test_summary_must_stay_outside_checkpoint_directory(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            source, report_dir, _ = write_settings(directory)
            settings = harness.load_settings(source)
            with self.assertRaises(SmokeFailure):
                harness.run(
                    settings, report_dir / "summary.json", hook_runner=hook_result
                )
            self.assertEqual(list(report_dir.iterdir()), [])

    def test_cell_report_from_other_settings_cannot_be_resumed(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            source, _, _ = write_settings(directory)
            settings = harness.load_settings(source)
            profile, budget = capacity.EXPECTED_SCENARIOS[0]
            value = harness._cell_wrapper(
                settings, profile, budget, scenario(profile, budget)
            )
            value["settings_sha256"] = "0" * 64
            path = harness.cell_report_path(settings, profile, budget)
            harness._write_private_json(path, value)
            with self.assertRaises(SmokeFailure):
                harness.plan(settings)

    def test_expected_name_symlink_is_refused_before_any_hook(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            source, _, _ = write_settings(directory)
            settings = harness.load_settings(source)
            profile, budget = capacity.EXPECTED_SCENARIOS[-1]
            path = harness.cell_report_path(settings, profile, budget)
            path.symlink_to(Path(directory) / "missing-target")
            calls = []
            def forbidden(*args):
                calls.append(args)
                raise AssertionError("hooks must not run")
            with self.assertRaises(SmokeFailure):
                harness.run(
                    settings, Path(directory) / "summary.json", hook_runner=forbidden
                )
            self.assertEqual(calls, [])


if __name__ == "__main__":
    unittest.main()
