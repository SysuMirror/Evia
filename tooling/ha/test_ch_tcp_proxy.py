import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from ch_tcp_proxy import Backend, BackendSelector


class BackendSelectorConstructionTest(unittest.TestCase):
    def test_from_specs_parses_host_port(self):
        selector = BackendSelector.from_specs(["10.0.0.1:8123", "10.0.0.2:8123"])
        self.assertEqual([b.address for b in selector.backends], ["10.0.0.1:8123", "10.0.0.2:8123"])
        self.assertTrue(all(b.healthy for b in selector.backends))

    def test_rejects_empty_and_malformed_specs(self):
        with self.assertRaises(ValueError):
            BackendSelector.from_specs([])
        for spec in ["10.0.0.1", "10.0.0.1:", ":8123", "10.0.0.1:0", "10.0.0.1:65536", "10.0.0.1:abc"]:
            with self.assertRaises(ValueError):
                BackendSelector.from_specs([spec])

    def test_rejects_non_positive_thresholds(self):
        with self.assertRaises(ValueError):
            BackendSelector([Backend("h", 1)], unhealthy_threshold=0)
        with self.assertRaises(ValueError):
            BackendSelector([Backend("h", 1)], healthy_threshold=0)


class FailoverStateMachineTest(unittest.TestCase):
    def setUp(self):
        self.selector = BackendSelector.from_specs(["a:8123", "b:8123", "c:8123"])
        self.a, self.b, self.c = self.selector.backends

    def test_pick_prefers_first_healthy_in_order(self):
        self.assertIs(self.selector.pick(), self.a)
        self.a.healthy = False
        self.assertIs(self.selector.pick(), self.b)
        self.b.healthy = False
        self.assertIs(self.selector.pick(), self.c)

    def test_all_down_returns_none(self):
        for backend in self.selector.backends:
            backend.healthy = False
        self.assertIsNone(self.selector.pick())
        self.assertEqual(self.selector.healthy_addresses(), [])

    def test_failure_threshold_marks_down_only_after_bound(self):
        self.selector.record_failure(self.a)
        self.assertTrue(self.a.healthy)
        self.assertEqual(self.a.consecutive_failures, 1)
        self.selector.record_failure(self.a)
        self.assertFalse(self.a.healthy)
        # Failover is immediate: the next pick is backend b.
        self.assertIs(self.selector.pick(), self.b)

    def test_success_resets_failure_counter_before_threshold(self):
        self.selector.record_failure(self.a)
        self.selector.record_success(self.a)
        self.assertEqual(self.a.consecutive_failures, 0)
        self.assertTrue(self.a.healthy)

    def test_recovery_requires_consecutive_successes(self):
        self.a.healthy = False
        self.selector.record_success(self.a)
        self.assertFalse(self.a.healthy, "one probe must not flap a backend back")
        self.selector.record_success(self.a)
        self.assertTrue(self.a.healthy)
        self.assertIs(self.selector.pick(), self.a)

    def test_recovered_backend_keeps_ordering(self):
        self.a.healthy = False
        self.b.healthy = False
        self.assertIs(self.selector.pick(), self.c)
        for _ in range(2):
            self.selector.record_success(self.a)
        self.assertIs(self.selector.pick(), self.a, "recovered primary must reclaim first position")

    def test_failure_after_recovery_restarts_counting(self):
        self.a.healthy = False
        for _ in range(2):
            self.selector.record_success(self.a)
        self.assertTrue(self.a.healthy)
        self.selector.record_failure(self.a)
        self.assertEqual(self.a.consecutive_failures, 1)
        self.assertEqual(self.a.consecutive_successes, 0)
        self.assertTrue(self.a.healthy, "a single new failure must not immediately re-mark down")

    def test_threshold_of_one_fails_fast(self):
        selector = BackendSelector.from_specs(["a:8123", "b:8123"], unhealthy_threshold=1)
        selector.record_failure(selector.backends[0])
        self.assertFalse(selector.backends[0].healthy)
        self.assertIs(selector.pick(), selector.backends[1])


if __name__ == "__main__":
    unittest.main()
