"""Pure-logic tests for the ClickHouse backup/restore drill; no server required."""
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))

import clickhouse_drill as chd  # noqa: E402


class IdentifierTest(unittest.TestCase):
    def test_identifier_guard_accepts_and_rejects(self):
        for good in ("evia", "evia_test", "_private1"):
            chd.validate_identifier(good)
        for bad in ("evia;DROP TABLE x", "evia-test", "1evia", "evia`x", ""):
            with self.assertRaises(ValueError):
                chd.validate_identifier(bad)

    def test_scratch_allowlist(self):
        chd.validate_scratch_name("evia_ch_restore_drill_ab12cd34ef56")
        for bad in ("evia", "evia_ch_restore_drill_xyz", "evia_restore_drill_ab12cd",
                    "evia_ch_restore_drill_" + "a" * 33):
            with self.assertRaises(ValueError):
                chd.validate_scratch_name(bad)

    def test_generated_names_pass_their_patterns(self):
        self.assertRegex(chd.new_backup_name(), chd.BACKUP_NAME_PATTERN)
        scratch = chd.new_scratch_name()
        chd.validate_scratch_name(scratch)
        self.assertNotEqual(chd.new_backup_name(), chd.new_backup_name())


class SqlBuilderTest(unittest.TestCase):
    def test_list_and_fingerprint_sql(self):
        self.assertIn("system.tables", chd.list_tables_sql("evia"))
        sql = chd.fingerprint_sql("evia", "event_facts")
        self.assertIn("count()", sql)
        self.assertIn("coalesce(sum(cityHash64(*)), 0)", sql)
        self.assertIn("evia.event_facts", sql)

    def test_backup_and_restore_sql_shapes(self):
        name = chd.new_backup_name()
        backup = chd.backup_sql("evia", name)
        self.assertEqual(backup, f"BACKUP DATABASE evia TO File('{name}')")
        restore = chd.restore_sql("evia", "evia_ch_restore_drill_abc123", name)
        self.assertIn("RESTORE DATABASE evia AS evia_ch_restore_drill_abc123", restore)
        self.assertIn(f"FROM File('{name}')", restore)
        self.assertEqual(
            chd.drop_database_sql("evia_ch_restore_drill_abc123"),
            "DROP DATABASE evia_ch_restore_drill_abc123 SYNC",
        )

    def test_unmanaged_backup_name_refused(self):
        with self.assertRaises(ValueError):
            chd.backup_sql("evia", "../etc/passwd")
        with self.assertRaises(ValueError):
            chd.restore_sql("evia", "evia_ch_restore_drill_abc123", "anything")

    def test_sql_injection_identifier_rejected(self):
        with self.assertRaises(ValueError):
            chd.fingerprint_sql("evia', '') OR 1=1 --", "event_facts")


class ClientArgvTest(unittest.TestCase):
    def test_docker_argv_forwards_password_via_env_not_argv(self):
        client = chd.CHClient(
            argv_prefix=["docker", "exec", "clickhouse-1", "clickhouse-client"],
            username="evia",
            password="secret",
        )
        argv = client.query_argv("SELECT 1")
        self.assertEqual(
            argv,
            ["docker", "exec", "-e", "CLICKHOUSE_PASSWORD", "clickhouse-1",
             "clickhouse-client", "--user", "evia", "--query", "SELECT 1"],
        )
        self.assertNotIn("secret", argv)
        self.assertEqual(client.environment()["CLICKHOUSE_PASSWORD"], "secret")
        self.assertTrue(client.is_docker)

    def test_native_argv_has_no_docker_env_flag(self):
        client = chd.CHClient(
            argv_prefix=["clickhouse-client"], username="evia", password="secret"
        )
        argv = client.query_argv("SELECT 1")
        self.assertEqual(argv, ["clickhouse-client", "--user", "evia", "--query", "SELECT 1"])
        self.assertNotIn("-e", argv)
        self.assertFalse(client.is_docker)

    def test_parse_client_defaults_and_env(self):
        with mock.patch.dict(os.environ, {"EVIA_CH_USERNAME": "u", "EVIA_CH_PASSWORD": "p"}):
            client = chd.parse_client(None, None, None)
        self.assertEqual(client.username, "u")
        self.assertEqual(client.password, "p")
        self.assertEqual(client.argv_prefix[0], "docker")
        custom = chd.parse_client("clickhouse-client --host ch", "u", "p")
        self.assertEqual(custom.argv_prefix, ["clickhouse-client", "--host", "ch"])

    def test_missing_username_rejected(self):
        client = chd.CHClient(["clickhouse-client"], "", "p")
        with self.assertRaises(ValueError):
            client.query_argv("SELECT 1")


class DockerCopyTest(unittest.TestCase):
    def test_tar_stream_argv_and_container_extraction(self):
        prefix = ["docker", "exec", "clickhouse-1", "clickhouse-client"]
        self.assertEqual(chd.container_from_prefix(prefix), "clickhouse-1")
        self.assertEqual(
            chd.tar_export_argv("clickhouse-1", "/var/lib/clickhouse/backups", "b"),
            ["docker", "exec", "clickhouse-1", "tar", "-C",
             "/var/lib/clickhouse/backups", "-cf", "-", "b"],
        )
        self.assertEqual(
            chd.tar_import_argv("clickhouse-1", "/var/lib/clickhouse/backups"),
            ["docker", "exec", "-i", "clickhouse-1", "tar", "-C",
             "/var/lib/clickhouse/backups", "-xf", "-"],
        )
        with self.assertRaises(ValueError):
            chd.container_from_prefix(["clickhouse-client"])


class SnapshotCompareTest(unittest.TestCase):
    def snapshot(self, *rows):
        return {"database": "evia", "tables": {f"t{i}": {"rows": r, "hash": h}
                                               for i, (r, h) in enumerate(rows)}}

    def test_identical_snapshots_pass(self):
        snap = self.snapshot((3, 99), (0, 0))
        self.assertEqual(chd.compare_snapshots(snap, json.loads(json.dumps(snap))), [])

    def test_missing_and_extra_tables(self):
        source = self.snapshot((3, 99))
        target = {"database": "evia", "tables": {}}
        problems = chd.compare_snapshots(source, target)
        self.assertTrue(any("missing from restored" in p for p in problems))
        target["tables"]["t9"] = {"rows": 1, "hash": 1}
        problems = chd.compare_snapshots(source, target)
        self.assertTrue(any("unexpected table" in p for p in problems))

    def test_row_count_and_hash_mismatches(self):
        source = self.snapshot((3, 99), (5, 77))
        count_diff = self.snapshot((2, 99), (5, 77))
        self.assertTrue(any("row count differs" in p for p in
                            chd.compare_snapshots(source, count_diff)))
        hash_diff = self.snapshot((3, 98), (5, 77))
        self.assertTrue(any("content hash differs" in p for p in
                            chd.compare_snapshots(source, hash_diff)))


class ManifestTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.artifact = Path(self.tmp.name) / "evia_ch_backup_20260101t000000_ab12cd"
        self.artifact.mkdir()
        (self.artifact / "data.bin").write_bytes(b"evia backup payload")

    def test_roundtrip_and_tamper_detection(self):
        snapshot = {"database": "evia", "tables": {"t": {"rows": 1, "hash": 7}}}
        manifest = chd.build_manifest("evia", self.artifact.name, self.artifact, snapshot)
        chd.verify_artifact(manifest, self.artifact)
        # Tampered content is detected.
        (self.artifact / "data.bin").write_bytes(b"tampered")
        with self.assertRaises(ValueError):
            chd.verify_artifact(manifest, self.artifact)
        (self.artifact / "data.bin").write_bytes(b"evia backup payload")
        # An extra undeclared file is detected.
        (self.artifact / "rogue.txt").write_text("x")
        with self.assertRaises(ValueError):
            chd.verify_artifact(manifest, self.artifact)
        (self.artifact / "rogue.txt").unlink()
        # A removed file is detected.
        (self.artifact / "data.bin").unlink()
        with self.assertRaises(ValueError):
            chd.verify_artifact(manifest, self.artifact)

    def test_manifest_rejects_foreign_version(self):
        manifest = chd.build_manifest("evia", self.artifact.name, self.artifact,
                                      {"database": "evia", "tables": {}})
        manifest["format_version"] = 99
        with self.assertRaises(ValueError):
            chd.verify_artifact(manifest, self.artifact)

    def test_empty_artifact_rejected(self):
        empty = Path(self.tmp.name) / "empty"
        empty.mkdir()
        with self.assertRaises(ValueError):
            chd.artifact_files(empty)


class CommandParsingTest(unittest.TestCase):
    def test_run_command_wraps_nonzero_exit(self):
        completed = mock.Mock(returncode=2, stdout="", stderr="boom")
        with mock.patch("subprocess.run", return_value=completed):
            with self.assertRaises(RuntimeError) as ctx:
                chd.run_command(["false"], {}, 5)
        self.assertIn("exited 2", str(ctx.exception))
        self.assertIn("boom", str(ctx.exception))

    def test_take_snapshot_parses_tab_separated_fingerprints(self):
        client = chd.CHClient(["clickhouse-client"], "evia", "")
        replies = {
            chd.list_tables_sql("evia"): "event_facts\nevent_facts_raw\n",
            chd.fingerprint_sql("evia", "event_facts"): "42\t987654321\n",
            chd.fingerprint_sql("evia", "event_facts_raw"): "0\t0\n",
        }
        with mock.patch.object(chd, "run_sql", side_effect=lambda c, sql: replies[sql]):
            snapshot = chd.take_snapshot(client, "evia")
        self.assertEqual(snapshot["tables"]["event_facts"], {"rows": 42, "hash": 987654321})
        self.assertEqual(snapshot["tables"]["event_facts_raw"], {"rows": 0, "hash": 0})

    def test_database_exists(self):
        client = chd.CHClient(["clickhouse-client"], "evia", "")
        with mock.patch.object(chd, "run_sql",
                               return_value="evia\nevium_ch_restore_drill_ab12cd\n"):
            self.assertTrue(chd.database_exists(client, "evia"))
            self.assertFalse(chd.database_exists(client, "other"))


if __name__ == "__main__":
    unittest.main()
