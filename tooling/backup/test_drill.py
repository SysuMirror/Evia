"""Pure-logic tests for the backup/restore drill; no PostgreSQL required."""
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import drill  # noqa: E402

DSN = "postgres://evia:secret@db.example:55432/evia_test?sslmode=disable"


class DsnTest(unittest.TestCase):
    def test_parse_and_redact(self):
        info = drill.parse_dsn(DSN)
        self.assertEqual((info.host, info.port, info.dbname, info.user, info.password, info.sslmode),
                         ("db.example", 55432, "evia_test", "evia", "secret", "disable"))
        redacted = drill.redact_dsn(DSN)
        self.assertIn("***", redacted)
        self.assertNotIn("secret", redacted)
        # A DSN without a password is returned unchanged.
        no_password = "postgres://evia@db.example:55432/evia"
        self.assertEqual(drill.redact_dsn(no_password), no_password)

    def test_parse_rejects_bad_inputs(self):
        for bad in ("mysql://u:p@h/db", "postgres://u@h:5432", "postgres://h:5432/db"):
            with self.assertRaises(ValueError):
                drill.parse_dsn(bad)

    def test_default_port_and_sslmode(self):
        info = drill.parse_dsn("postgres://u:p@h/db")
        self.assertEqual(info.port, 5432)
        self.assertEqual(info.sslmode, "prefer")

    def test_with_database(self):
        info = drill.parse_dsn(DSN).with_database("evia_restore_drill_ab12cd34ef56")
        self.assertEqual(info.dbname, "evia_restore_drill_ab12cd34ef56")
        self.assertEqual(info.password, "secret")


class LibpqEnvTest(unittest.TestCase):
    def test_password_uses_env_not_argv(self):
        env = drill.libpq_env(drill.parse_dsn(DSN))
        self.assertEqual(env["PGPASSWORD"], "secret")
        self.assertEqual(env["PGDATABASE"], "evia_test")
        self.assertEqual(env["PGHOST"], "db.example")
        for cmd in (drill.pg_dump_command(Path("x.dump")),
                    drill.pg_restore_command(Path("x.dump"), "evia_restore_drill_ab12cd34ef56"),
                    drill.psql_command("SELECT 1")):
            self.assertFalse(any("secret" in part for part in cmd))


class ScratchNameTest(unittest.TestCase):
    def test_allowlist(self):
        drill.validate_scratch_database_name("evia_restore_drill_ab12cd34ef56")
        for bad in ("evia", "postgres", "evia_test", "evia_restore_drill_",
                    "evia_restore_drill_ABC123", "evia_restore_drill_" + "a" * 33,
                    "evia_restore_drill_../x"):
            with self.assertRaises(ValueError):
                drill.validate_scratch_database_name(bad)

    def test_generated_name_passes_allowlist(self):
        for _ in range(10):
            drill.validate_scratch_database_name(drill.new_scratch_database_name())


class CommandBuilderTest(unittest.TestCase):
    def test_pg_dump_flags(self):
        cmd = drill.pg_dump_command(Path("/tmp/backup.dump"))
        self.assertEqual(cmd[:2], ["pg_dump", "-Fc"])
        self.assertIn("--no-owner", cmd)
        self.assertIn("--no-privileges", cmd)
        self.assertEqual(cmd[-2:], ["--file", "/tmp/backup.dump"])

    def test_pg_restore_exit_on_error(self):
        cmd = drill.pg_restore_command(Path("/tmp/backup.dump"), "evia_restore_drill_ab12cd34ef56")
        self.assertIn("--exit-on-error", cmd)
        self.assertIn("--clean", cmd)
        self.assertIn("--if-exists", cmd)
        self.assertEqual(cmd[-3:-1], ["-d", "evia_restore_drill_ab12cd34ef56"])
        self.assertEqual(cmd[-1], "/tmp/backup.dump")
        with self.assertRaises(ValueError):
            drill.pg_restore_command(Path("/tmp/backup.dump"), "evia; DROP TABLE accounts")

    def test_identifier_guards(self):
        with self.assertRaises(ValueError):
            drill.pg_dump_command(Path("x.dump"), schema="public; DROP TABLE accounts")
        with self.assertRaises(ValueError):
            drill.table_fingerprint_sql("public", "accounts; SELECT 1")


class FingerprintSqlTest(unittest.TestCase):
    def test_sql_shape(self):
        sql = drill.table_fingerprint_sql("public", "accounts")
        self.assertIn('"public"."accounts"', sql)
        self.assertIn("hashtextextended", sql)
        self.assertIn("count(*)", sql)
        self.assertIn("schema_migrations", drill.ledger_sql())


def snapshot(tables, ledger=None):
    return {
        "database": "src",
        "schema": "public",
        "captured_at": "2026-11-12T00:00:00+00:00",
        "tables": tables,
        "ledger": ledger or [["001_access.sql", "abc"], ["002_catalog.sql", "def"]],
    }


class CompareSnapshotsTest(unittest.TestCase):
    def setUp(self):
        self.base = snapshot({
            "accounts": {"rows": 2, "hash": 100},
            "projects": {"rows": 1, "hash": 50},
        })

    def test_identical_snapshots_pass(self):
        self.assertEqual(drill.compare_snapshots(self.base, json.loads(json.dumps(self.base))), [])

    def test_missing_and_extra_tables(self):
        target = snapshot({"accounts": {"rows": 2, "hash": 100}})
        findings = drill.compare_snapshots(self.base, target)
        self.assertTrue(any("missing table" in f and "projects" in f for f in findings))

    def test_row_count_mismatch(self):
        target = snapshot({
            "accounts": {"rows": 1, "hash": 100},
            "projects": {"rows": 1, "hash": 50},
        })
        findings = drill.compare_snapshots(self.base, target)
        self.assertEqual(len(findings), 1)
        self.assertIn("row count differs for accounts", findings[0])

    def test_content_hash_mismatch_with_equal_counts(self):
        target = snapshot({
            "accounts": {"rows": 2, "hash": 999},
            "projects": {"rows": 1, "hash": 50},
        })
        findings = drill.compare_snapshots(self.base, target)
        self.assertEqual(len(findings), 1)
        self.assertIn("content hash differs for accounts", findings[0])

    def test_ledger_checksum_mismatch(self):
        target = snapshot(
            {"accounts": {"rows": 2, "hash": 100}, "projects": {"rows": 1, "hash": 50}},
            ledger=[["001_access.sql", "tampered"], ["002_catalog.sql", "def"]],
        )
        findings = drill.compare_snapshots(self.base, target)
        self.assertTrue(any("migration checksum differs for 001_access.sql" in f for f in findings))

    def test_ledger_missing_entry(self):
        target = snapshot(
            {"accounts": {"rows": 2, "hash": 100}, "projects": {"rows": 1, "hash": 50}},
            ledger=[["001_access.sql", "abc"]],
        )
        findings = drill.compare_snapshots(self.base, target)
        self.assertTrue(any("ledger entry missing" in f for f in findings))


class ManifestTest(unittest.TestCase):
    def test_roundtrip_and_tamper_detection(self):
        with tempfile.TemporaryDirectory() as tmp:
            artifact = Path(tmp) / "backup.dump"
            artifact.write_bytes(b"dump-bytes")
            manifest = drill.build_manifest(DSN, artifact, snapshot({"accounts": {"rows": 1, "hash": 7}}))
            self.assertEqual(manifest["format_version"], drill.MANIFEST_FORMAT_VERSION)
            self.assertEqual(manifest["artifact_sha256"], drill.sha256_file(artifact))
            self.assertNotIn("secret", json.dumps(manifest))
            drill.verify_artifact(manifest, artifact)
            manifest_path = Path(tmp) / "backup.manifest.json"
            drill.write_json(manifest_path, manifest)
            reloaded = json.loads(manifest_path.read_text())
            self.assertEqual(reloaded["artifact_sha256"], manifest["artifact_sha256"])
            artifact.write_bytes(b"tampered")
            with self.assertRaises(ValueError):
                drill.verify_artifact(manifest, artifact)

    def test_manifest_rejects_foreign_artifact(self):
        with tempfile.TemporaryDirectory() as tmp:
            artifact = Path(tmp) / "backup.dump"
            artifact.write_bytes(b"x")
            manifest = drill.build_manifest(DSN, artifact, snapshot({}))
            other = Path(tmp) / "other.dump"
            other.write_bytes(b"x")
            with self.assertRaises(ValueError):
                drill.verify_artifact(manifest, other)


class CommandFailureTest(unittest.TestCase):
    def test_missing_client_tool_is_runtime_error(self):
        with tempfile.TemporaryDirectory() as tmp:
            env = dict(os.environ)
            env["PATH"] = tmp
            with self.assertRaises(RuntimeError):
                drill.run_command(["psql", "-c", "SELECT 1"], env)


if __name__ == "__main__":
    unittest.main()
