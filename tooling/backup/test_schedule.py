"""Pure-logic tests for the scheduled backup orchestrator; no containers needed."""
import fcntl
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import schedule  # noqa: E402


def write_config(directory: Path, targets: list[dict], *, version=schedule.SCHEDULE_FORMAT_VERSION) -> Path:
    path = directory / "config.json"
    path.write_text(
        json.dumps(
            {
                "version": version,
                "state_dir": str(directory / "state"),
                "metrics_path": str(directory / "metrics" / "evia-backup.prom"),
                "targets": targets,
            }
        )
    )
    return path


def pg_target(name: str = "control-pg", **overrides) -> dict:
    target = {
        "name": name,
        "kind": "postgres",
        "output_dir": "/tmp/evia-backup-pg",
        "retention_keep": 3,
        "dsn_env": "EVIA_TEST_PG_DSN",
    }
    target.update(overrides)
    return target


def ch_target(name: str = "events-ch", **overrides) -> dict:
    target = {
        "name": name,
        "kind": "clickhouse",
        "output_dir": "/tmp/evia-backup-ch",
        "retention_keep": 2,
        "database": "evia_events",
    }
    target.update(overrides)
    return target


class ConfigValidationTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def test_accepts_valid_mixed_targets(self):
        path = write_config(self.root, [pg_target(), ch_target()])
        targets, state_dir, metrics_path, replication = schedule.load_config(path)
        self.assertIsNone(replication)
        self.assertEqual([t.name for t in targets], ["control-pg", "events-ch"])
        self.assertEqual(targets[0].kind, "postgres")
        self.assertEqual(targets[1].database, "evia_events")
        self.assertEqual(state_dir, self.root / "state")
        self.assertTrue(str(metrics_path).endswith(".prom"))

    def test_rejects_bad_versions_and_bodies(self):
        cases = [
            {"version": 99, "state_dir": "s", "metrics_path": "m", "targets": [pg_target()]},
            {"version": 1, "state_dir": "s", "metrics_path": "m", "targets": []},
            {"version": 1, "state_dir": "s", "metrics_path": "m", "targets": "not-a-list"},
            {"version": 1, "state_dir": "s", "metrics_path": "m",
             "targets": [pg_target(f"bad-name-{i}") for i in range(schedule.MAX_TARGETS + 1)]},
            # Explicit missing-field case:
            {"version": 1, "targets": [pg_target()]},
        ]
        for document in cases:
            path = self.root / "bad.json"
            path.write_text(json.dumps(document))
            with self.assertRaises(schedule.ScheduleError):
                schedule.load_config(path)

    def test_rejects_target_field_violations(self):
        bad_targets = [
            pg_target("UPPER"),
            pg_target(output_dir=""),
            pg_target(retention_keep=0),
            pg_target(retention_keep=101),
            pg_target(dsn_env="lower-case"),
            pg_target(schema="1bad-schema"),
            ch_target(database="bad-db!"),
            ch_target(ch_client_env="lower"),
            ch_target(timeout_seconds=10),
            {"name": "unknown", "kind": "mysql", "output_dir": "/tmp/x"},
        ]
        for raw in bad_targets:
            path = write_config(self.root, [raw])
            with self.assertRaises(schedule.ScheduleError):
                schedule.load_config(path)

    def test_rejects_duplicate_names(self):
        path = write_config(self.root, [pg_target("dup"), ch_target("dup")])
        with self.assertRaises(schedule.ScheduleError):
            schedule.load_config(path)

    def test_missing_config_raises_schedule_error(self):
        with self.assertRaises(schedule.ScheduleError):
            schedule.load_config(self.root / "absent.json")


class RunOnceTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        os.environ["EVIA_TEST_PG_DSN"] = "postgres://evia:secret@db.example:5432/evia"
        os.environ["EVIA_TEST_CH_PASSWORD"] = "ch-secret"

    def tearDown(self):
        self.tmp.cleanup()
        os.environ.pop("EVIA_TEST_PG_DSN", None)
        os.environ.pop("EVIA_TEST_CH_PASSWORD", None)

    def _config(self, targets):
        return write_config(self.root, targets)

    def test_success_writes_state_metrics_and_report(self):
        config = self._config([
            pg_target(output_dir=str(self.root / "pg")),
            ch_target(output_dir=str(self.root / "ch")),
        ])
        calls = []

        def fake_runner(target):
            calls.append(target.name)
            return {"ok": True, "target": target.name}

        code = schedule.run_once(config, {"postgres": fake_runner, "clickhouse": fake_runner})
        self.assertEqual(code, 0)
        self.assertEqual(calls, ["control-pg", "events-ch"])

        state = json.loads((self.root / "state" / "schedule-state.json").read_text())
        self.assertEqual(state["version"], schedule.STATE_FORMAT_VERSION)
        for name in calls:
            entry = state["targets"][name]
            self.assertEqual(entry["up"], 1)
            self.assertEqual(entry["success_total"], 1)
            self.assertEqual(entry["failure_total"], 0)
            self.assertGreater(entry["last_attempt_ts"], 0)
            self.assertEqual(entry["last_attempt_ts"], entry["last_success_ts"])
            self.assertGreaterEqual(entry["last_duration_seconds"], 0.0)

        metrics = (self.root / "metrics" / "evia-backup.prom").read_text()
        for metric in schedule.METRIC_SPECS:
            self.assertIn(f"# TYPE {metric} {schedule.METRIC_SPECS[metric][0]}", metrics)
        self.assertIn('target="control-pg",kind="postgres"', metrics)
        self.assertIn('evia_backup_up{target="control-pg",kind="postgres"} 1', metrics)
        reports = list((self.root / "state").glob("schedule-run-*.json"))
        self.assertEqual(len(reports), 1)

    def test_second_run_accumulates_counters(self):
        config = self._config([pg_target(output_dir=str(self.root / "pg"))])
        runner = lambda target: {"ok": True}  # noqa: E731
        self.assertEqual(schedule.run_once(config, {"postgres": runner}), 0)
        self.assertEqual(schedule.run_once(config, {"postgres": runner}), 0)
        state = json.loads((self.root / "state" / "schedule-state.json").read_text())
        entry = state["targets"]["control-pg"]
        self.assertEqual(entry["success_total"], 2)
        self.assertEqual(entry["failure_total"], 0)

    def test_failure_fails_closed_redacts_secret_and_continues(self):
        config = self._config([
            pg_target(output_dir=str(self.root / "pg")),
            ch_target(output_dir=str(self.root / "ch")),
        ])

        def pg_fail(target):
            raise RuntimeError("connection refused with password secret in the DSN")

        def ch_ok(target):
            return {"ok": True}

        code = schedule.run_once(config, {"postgres": pg_fail, "clickhouse": ch_ok})
        self.assertEqual(code, 1)
        state = json.loads((self.root / "state" / "schedule-state.json").read_text())
        self.assertEqual(state["targets"]["control-pg"]["up"], 0)
        self.assertEqual(state["targets"]["control-pg"]["failure_total"], 1)
        self.assertEqual(state["targets"]["events-ch"]["up"], 1)
        report = next((self.root / "state").glob("schedule-run-*.json"))
        body = report.read_text()
        self.assertNotIn("secret", body)
        self.assertIn("***", body)
        metrics = (self.root / "metrics" / "evia-backup.prom").read_text()
        self.assertIn('evia_backup_up{target="control-pg",kind="postgres"} 0', metrics)

    def test_overlapping_run_is_rejected(self):
        config = self._config([pg_target(output_dir=str(self.root / "pg"))])
        lock_path = self.root / "state" / "schedule.lock"
        lock_path.parent.mkdir(parents=True, exist_ok=True)
        with lock_path.open("w") as handle:
            fcntl.flock(handle.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
            with self.assertRaises(schedule.ScheduleError):
                schedule.run_once(config, {"postgres": lambda target: {}})

    def test_malformed_state_refuses_to_reset_counters(self):
        config = self._config([pg_target(output_dir=str(self.root / "pg"))])
        state_dir = self.root / "state"
        state_dir.mkdir(parents=True)
        (state_dir / "schedule-state.json").write_text('{"version": 99, "targets": {}}')
        with self.assertRaises(schedule.ScheduleError):
            schedule.run_once(config, {"postgres": lambda target: {}})


class PruneTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def test_prunes_only_managed_pg_entries_and_keeps_newest(self):
        for stamp in ("20260101T000000Z", "20260102T000000Z", "20260103T000000Z", "20260104T000000Z"):
            (self.root / f"evia-pg-backup-{stamp}.dump").write_text("x")
            (self.root / f"evia-pg-backup-{stamp}.manifest.json").write_text("{}")
            (self.root / f"evia-restore-drill-{stamp}.report.json").write_text("{}")
        (self.root / "unrelated-notes.txt").write_text("keep me")
        target = schedule.Target(
            name="control-pg", kind="postgres", output_dir=self.root, retention_keep=2,
        )
        removed = schedule.prune_target(target)
        # Two oldest stems removed across three managed patterns.
        self.assertEqual(len(removed), 6)
        remaining = sorted(path.name for path in self.root.iterdir())
        self.assertIn("evia-pg-backup-20260104T000000Z.dump", remaining)
        self.assertIn("evia-pg-backup-20260103T000000Z.dump", remaining)
        self.assertNotIn("evia-pg-backup-20260101T000000Z.dump", remaining)
        self.assertIn("unrelated-notes.txt", remaining)

    def test_prunes_clickhouse_backup_dirs(self):
        for suffix in ("a1b2c3", "d4e5f6", "070809"):
            name = f"evia_ch_backup_20260101t00000{suffix[-1:]}_{suffix}"
            backup_dir = self.root / name
            backup_dir.mkdir()
            (backup_dir / "data.tar").write_text("x")
            (self.root / f"{name}.manifest.json").write_text("{}")
        (self.root / "manual-dump").mkdir()
        target = schedule.Target(
            name="events-ch", kind="clickhouse", output_dir=self.root, retention_keep=1,
        )
        schedule.prune_target(target)
        remaining = sorted(path.name for path in self.root.iterdir())
        self.assertIn("manual-dump", remaining)
        self.assertEqual(len([n for n in remaining if n.startswith("evia_ch_backup_")]), 2)  # dir + manifest


class TextfileRenderTest(unittest.TestCase):
    def test_render_is_sorted_and_label_bounded(self):
        state = {
            "targets": {
                "zebra": {
                    "kind": "clickhouse", "up": 1, "last_attempt_ts": 100, "last_success_ts": 90,
                    "success_total": 5, "failure_total": 1, "last_duration_seconds": 1.25,
                },
                "alpha": {
                    "kind": "postgres", "up": 0, "last_attempt_ts": 101, "last_success_ts": 0,
                    "success_total": 0, "failure_total": 2, "last_duration_seconds": 0.5,
                },
            }
        }
        text = schedule.render_textfile(state)
        self.assertTrue(text.endswith("\n"))
        # Helpers precede every series and metric declarations appear once each.
        for metric in schedule.METRIC_SPECS:
            self.assertEqual(text.count(f"# TYPE {metric}"), 1)
        alpha_line = text.index('target="alpha"')
        zebra_line = text.index('target="zebra"')
        self.assertLess(alpha_line, zebra_line)
        self.assertIn('evia_backup_success_total{target="zebra",kind="clickhouse"} 5', text)


if __name__ == "__main__":
    unittest.main()


class ReplicationHookTest(unittest.TestCase):
    """The optional replication stage runs after drills and fails closed."""

    def setUp(self):
        import threading
        from http.server import ThreadingHTTPServer
        from test_objectstore import ACCESS_KEY, MockS3Handler, SECRET_KEY

        MockS3Handler.objects = {}
        MockS3Handler.corrupt_get = False
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), MockS3Handler)
        self.port = self.server.server_address[1]
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        os.environ["EVIA_TEST_PG_DSN"] = "postgres://evia:secret@db.example:5432/evia"
        os.environ["EVIA_TEST_S3_AK"] = ACCESS_KEY
        os.environ["EVIA_TEST_S3_SK"] = SECRET_KEY
        (self.root / "pg").mkdir()

    def tearDown(self):
        self.server.shutdown()
        self.tmp.cleanup()
        os.environ.pop("EVIA_TEST_PG_DSN", None)
        os.environ.pop("EVIA_TEST_S3_AK", None)
        os.environ.pop("EVIA_TEST_S3_SK", None)

    def _write_configs(self, replication_document: dict) -> Path:
        replication_path = self.root / "replication.json"
        replication_path.write_text(json.dumps(replication_document))
        schedule_path = self.root / "schedule.json"
        schedule_path.write_text(json.dumps({
            "version": schedule.SCHEDULE_FORMAT_VERSION,
            "state_dir": str(self.root / "state"),
            "metrics_path": str(self.root / "metrics" / "evia-backup.prom"),
            "replication_config": str(replication_path),
            "targets": [pg_target(output_dir=str(self.root / "pg"))],
        }))
        return schedule_path

    def test_replication_stage_runs_and_reports_outcome(self):
        replication = {
            "version": 1,
            "state_dir": str(self.root / "repl-state"),
            "store": {
                "endpoint": f"http://127.0.0.1:{self.port}",
                "region": "us-east-1",
                "bucket": "evia-backup",
                "access_key_env": "EVIA_TEST_S3_AK",
                "secret_key_env": "EVIA_TEST_S3_SK",
                "path_style": True,
                "insecure_http": True,
                "timeout_seconds": 5,
            },
            "sources": [{
                "name": "control-pg",
                "kind": "postgres",
                "directory": str(self.root / "pg"),
                "prefix": "postgres/",
                "retention_keep": 2,
            }],
        }
        config = self._write_configs(replication)
        drill_ok = lambda target: {"ok": True}  # noqa: E731
        code = schedule.run_once(config, {"postgres": drill_ok, "clickhouse": drill_ok})
        # Nothing to replicate yet, so the stage succeeds even with no objects.
        self.assertEqual(code, 0)
        run_reports = list((self.root / "state").glob("schedule-run-*.json"))
        report = json.loads(run_reports[0].read_text())
        self.assertEqual(report["replication"]["exit_code"], 0)
        self.assertEqual(report["replication"]["bucket"], "evia-backup")

    def test_unreachable_store_fails_the_whole_run(self):
        replication = {
            "version": 1,
            "state_dir": str(self.root / "repl-state"),
            "store": {
                "endpoint": "http://127.0.0.1:9",
                "region": "us-east-1",
                "bucket": "evia-backup",
                "access_key_env": "EVIA_TEST_S3_AK",
                "secret_key_env": "EVIA_TEST_S3_SK",
                "path_style": True,
                "insecure_http": True,
                "timeout_seconds": 1,
            },
            "sources": [{
                "name": "control-pg",
                "kind": "postgres",
                "directory": str(self.root / "pg"),
                "prefix": "postgres/",
                "retention_keep": 2,
            }],
        }
        # Give the source a real file so the replicator attempts an upload.
        (self.root / "pg" / "evia-pg-backup-20260101T000000Z.dump").write_text("payload")
        config = self._write_configs(replication)
        drill_ok = lambda target: {"ok": True}  # noqa: E731
        code = schedule.run_once(config, {"postgres": drill_ok, "clickhouse": drill_ok})
        self.assertEqual(code, 1)
        run_reports = list((self.root / "state").glob("schedule-run-*.json"))
        report = json.loads(run_reports[0].read_text())
        self.assertEqual(report["replication"]["exit_code"], 1)
        self.assertIn("error", report["replication"]["sources"][0])
