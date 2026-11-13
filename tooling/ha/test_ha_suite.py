import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from ha_suite import (
    DRILLS,
    DEV_CLICKHOUSE_ENV,
    SuiteError,
    TIER_DEV_STACK,
    TIER_TOPOLOGY,
    evaluate_preconditions,
    missing_env,
    select_drills,
    summarize,
    validate_registry,
)


def drill(name, tier=TIER_TOPOLOGY, required_env=(), timeout=100):
    return {"name": name, "script": f"{name}.py", "tier": tier,
            "compose": None, "required_env": tuple(required_env),
            "timeout_seconds": timeout}


class RegistryValidationTest(unittest.TestCase):
    def test_shipped_registry_is_valid(self):
        self.assertTrue(validate_registry(DRILLS))
        names = [entry["name"] for entry in DRILLS]
        self.assertEqual(len(names), len(set(names)))
        self.assertEqual(len(DRILLS), 9)

    def test_rejects_duplicate_names(self):
        with self.assertRaises(SuiteError):
            validate_registry([drill("a"), drill("a")])

    def test_rejects_missing_key_and_bad_tier(self):
        bad = {"name": "x", "script": "x.py", "tier": "nope", "timeout_seconds": 10}
        with self.assertRaises(SuiteError):
            validate_registry([bad])

    def test_rejects_bad_timeout(self):
        entry = drill("x")
        entry["timeout_seconds"] = 0
        with self.assertRaises(SuiteError):
            validate_registry([entry])


class SelectionTest(unittest.TestCase):
    def setUp(self):
        self.registry = [
            drill("a", TIER_TOPOLOGY),
            drill("b", TIER_DEV_STACK),
            drill("c", TIER_TOPOLOGY),
        ]

    def test_only_filter(self):
        self.assertEqual([d["name"] for d in select_drills(self.registry, only=["b"])], ["b"])

    def test_exclude_filter(self):
        self.assertEqual(
            [d["name"] for d in select_drills(self.registry, exclude=["b"])], ["a", "c"])

    def test_tier_filter(self):
        selected = select_drills(self.registry, tier=TIER_DEV_STACK)
        self.assertEqual([d["name"] for d in selected], ["b"])

    def test_combined_filters(self):
        selected = select_drills(self.registry, only=["a", "b"], exclude=["a"],
                                 tier=TIER_DEV_STACK)
        self.assertEqual([d["name"] for d in selected], ["b"])

    def test_unknown_name_fails_closed(self):
        with self.assertRaises(SuiteError):
            select_drills(self.registry, only=["nope"])
        with self.assertRaises(SuiteError):
            select_drills(self.registry, exclude=["nope"])

    def test_empty_only_is_treated_as_unspecified(self):
        # An empty include list carries no name constraint, so the tier
        # filter still applies; main() never passes an empty list anyway.
        selected = select_drills(self.registry, only=[], tier=TIER_DEV_STACK)
        self.assertEqual([d["name"] for d in selected], ["b"])


class MissingEnvTest(unittest.TestCase):
    def test_detects_blank_and_missing(self):
        required = ("A", "B", "C")
        self.assertEqual(missing_env(required, {"A": "x", "B": ""}), ["B", "C"])
        self.assertEqual(missing_env(required, {"A": "1", "B": "2", "C": "3"}), [])


class PreconditionsTest(unittest.TestCase):
    def test_dev_stack_skips_without_credentials(self):
        entry = drill("d", TIER_DEV_STACK, required_env=DEV_CLICKHOUSE_ENV)
        runnable, reason = evaluate_preconditions(entry, {}, docker_available=False)
        self.assertFalse(runnable)
        self.assertIn("missing environment variables", reason)

    def test_dev_stack_runs_without_docker(self):
        entry = drill("d", TIER_DEV_STACK, required_env=DEV_CLICKHOUSE_ENV)
        env = {name: "present" for name in DEV_CLICKHOUSE_ENV}
        runnable, _ = evaluate_preconditions(entry, env, docker_available=False)
        self.assertTrue(runnable)

    def test_topology_requires_docker(self):
        entry = drill("t", TIER_TOPOLOGY)
        runnable, reason = evaluate_preconditions(entry, {}, docker_available=False)
        self.assertFalse(runnable)
        self.assertIn("Docker", reason)

    def test_topology_runs_when_docker_available(self):
        entry = drill("t", TIER_TOPOLOGY)
        runnable, _ = evaluate_preconditions(entry, {}, docker_available=True)
        self.assertTrue(runnable)


class SummarizeTest(unittest.TestCase):
    def result(self, name, status):
        return {"name": name, "status": status}

    def test_all_passing(self):
        summary = summarize([self.result("a", "passed"), self.result("b", "passed")])
        self.assertEqual(summary["verdict"], "passed")
        self.assertEqual(summary["passed"], 2)
        self.assertEqual(summary["failed"], 0)
        self.assertEqual(summary["total"], 2)

    def test_skips_do_not_fail_but_failures_do(self):
        summary = summarize([
            self.result("a", "passed"), self.result("b", "skipped")])
        self.assertEqual(summary["verdict"], "passed")
        self.assertEqual(summary["skipped"], 1)
        summary = summarize([
            self.result("a", "passed"), self.result("b", "failed"),
            self.result("c", "skipped")])
        self.assertEqual(summary["verdict"], "failed")
        self.assertEqual(summary["failed"], 1)

    def test_unknown_status_rejected(self):
        with self.assertRaises(SuiteError):
            summarize([self.result("a", "exploded")])


if __name__ == "__main__":
    unittest.main()
