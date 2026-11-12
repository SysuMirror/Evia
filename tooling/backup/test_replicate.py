"""Tests for offsite replication: state machine, idempotency and pruning."""
import json
import os
import sys
import tempfile
import threading
import unittest
from http.server import ThreadingHTTPServer
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import replicate  # noqa: E402
from test_objectstore import ACCESS_KEY, MockS3Handler, SECRET_KEY  # noqa: E402


class ConfigValidationTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def _write(self, document: dict) -> Path:
        path = self.root / "replication.json"
        path.write_text(json.dumps(document))
        return path

    def _base(self) -> dict:
        return {
            "version": 1,
            "state_dir": str(self.root / "state"),
            "store": {
                "endpoint": "http://127.0.0.1:9000",
                "region": "us-east-1",
                "bucket": "evia-backup",
                "access_key_env": "EVIA_TEST_S3_AK",
                "secret_key_env": "EVIA_TEST_S3_SK",
                "path_style": True,
                "insecure_http": True,
            },
            "sources": [
                {
                    "name": "control-pg",
                    "kind": "postgres",
                    "directory": str(self.root / "pg"),
                    "prefix": "postgres/",
                    "retention_keep": 2,
                }
            ],
        }

    def test_valid_config_loads(self):
        store, sources, state_dir = replicate.load_config(self._write(self._base()))
        self.assertEqual(store.bucket, "evia-backup")
        self.assertEqual(sources[0].name, "control-pg")
        self.assertEqual(state_dir, self.root / "state")

    def test_invalid_documents_are_rejected(self):
        cases = []
        bad_version = self._base()
        bad_version["version"] = 99
        cases.append(bad_version)
        missing_store = self._base()
        del missing_store["store"]
        cases.append(missing_store)
        empty_sources = self._base()
        empty_sources["sources"] = []
        cases.append(empty_sources)
        duplicate = self._base()
        duplicate["sources"].append(dict(duplicate["sources"][0]))
        cases.append(duplicate)
        bad_keep = self._base()
        bad_keep["sources"][0]["retention_keep"] = 0
        cases.append(bad_keep)
        bad_verify = self._base()
        bad_verify["sources"][0]["verify"] = "checksum-only"
        cases.append(bad_verify)
        no_state = self._base()
        del no_state["state_dir"]
        cases.append(no_state)
        for document in cases:
            with self.assertRaises(replicate.ReplicationError):
                replicate.load_config(self._write(document))

    def test_missing_config_raises_named_error(self):
        with self.assertRaises(replicate.ReplicationError):
            replicate.load_config(self.root / "absent.json")


class LoopbackReplicationTest(unittest.TestCase):
    def setUp(self):
        MockS3Handler.objects = {}
        MockS3Handler.corrupt_get = False
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), MockS3Handler)
        self.port = self.server.server_address[1]
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        os.environ["EVIA_TEST_S3_AK"] = ACCESS_KEY
        os.environ["EVIA_TEST_S3_SK"] = SECRET_KEY
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.pg_dir = self.root / "pg"
        self.pg_dir.mkdir()
        self.config_path = self.root / "replication.json"
        self.config_path.write_text(json.dumps({
            "version": 1,
            "state_dir": str(self.root / "state"),
            "store": {
                "endpoint": f"http://127.0.0.1:{self.port}",
                "region": "us-east-1",
                "bucket": "evia-backup",
                "access_key_env": "EVIA_TEST_S3_AK",
                "secret_key_env": "EVIA_TEST_S3_SK",
                "path_style": True,
                "insecure_http": True,
                "timeout_seconds": 10,
            },
            "sources": [
                {
                    "name": "control-pg",
                    "kind": "postgres",
                    "directory": str(self.pg_dir),
                    "prefix": "postgres/",
                    "retention_keep": 2,
                }
            ],
        }))

    def tearDown(self):
        self.server.shutdown()
        self.tmp.cleanup()
        os.environ.pop("EVIA_TEST_S3_AK", None)
        os.environ.pop("EVIA_TEST_S3_SK", None)

    def _dump(self, stamp: str, payload: bytes) -> Path:
        path = self.pg_dir / f"evia-pg-backup-{stamp}.dump"
        path.write_bytes(payload)
        manifest = self.pg_dir / f"evia-pg-backup-{stamp}.manifest.json"
        manifest.write_text("{}")
        return path

    def test_run_uploads_verifies_and_then_skips_unchanged(self):
        self._dump("20260101T000000Z", b"first backup")
        self.assertEqual(replicate.run_once(self.config_path), 0)

        state = json.loads((self.root / "state" / "replication-state.json").read_text())
        keys = list(state["objects"])
        self.assertEqual(len(keys), 2)  # dump + manifest
        self.assertTrue(all(key.startswith("postgres/") for key in keys))
        remote = MockS3Handler.objects
        self.assertEqual(remote["postgres/evia-pg-backup-20260101T000000Z.dump"], b"first backup")

        # Second run with identical artifacts must not re-upload.
        before = dict(remote)
        self.assertEqual(replicate.run_once(self.config_path), 0)
        self.assertEqual(before, remote)
        report = sorted((self.root / "state").glob("replication-run-*.json"))[-1]
        body = json.loads(report.read_text())
        self.assertEqual(len(body["sources"][0]["skipped"]), 2)
        self.assertEqual(body["sources"][0]["uploaded"], [])

    def test_changed_content_is_re_replicated_and_verified(self):
        path = self._dump("20260101T000000Z", b"v1")
        self.assertEqual(replicate.run_once(self.config_path), 0)
        path.write_bytes(b"v2 with different bytes")
        self.assertEqual(replicate.run_once(self.config_path), 0)
        key = "postgres/evia-pg-backup-20260101T000000Z.dump"
        self.assertEqual(MockS3Handler.objects[key], b"v2 with different bytes")
        state = json.loads((self.root / "state" / "replication-state.json").read_text())
        self.assertEqual(len(state["objects"][key]["sha256"]), 64)

    def test_remote_retention_prunes_beyond_keep_window(self):
        for stamp, payload in (
            ("20260101T000000Z", b"oldest"),
            ("20260102T000000Z", b"middle"),
            ("20260103T000000Z", b"newest"),
        ):
            self._dump(stamp, payload)
        self.assertEqual(replicate.run_once(self.config_path), 0)
        remote_keys = sorted(MockS3Handler.objects)
        # Remote retention mirrors the local keep window immediately: the
        # oldest pair is pruned during the first run, leaving two stems.
        self.assertEqual(len(remote_keys), 4)
        self.assertNotIn("postgres/evia-pg-backup-20260101T000000Z.dump", remote_keys)
        self.assertIn("postgres/evia-pg-backup-20260103T000000Z.dump", remote_keys)

        # Drop the now-oldest local pair; the next run must delete it remotely too.
        (self.pg_dir / "evia-pg-backup-20260101T000000Z.dump").unlink()
        (self.pg_dir / "evia-pg-backup-20260101T000000Z.manifest.json").unlink()
        self.assertEqual(replicate.run_once(self.config_path), 0)
        remaining = sorted(MockS3Handler.objects)
        self.assertNotIn("postgres/evia-pg-backup-20260101T000000Z.dump", remaining)
        self.assertIn("postgres/evia-pg-backup-20260103T000000Z.dump", remaining)
        state = json.loads((self.root / "state" / "replication-state.json").read_text())
        self.assertNotIn("postgres/evia-pg-backup-20260101T000000Z.dump", state["objects"])

    def test_corrupt_download_fails_closed_and_records_nothing(self):
        self._dump("20260101T000000Z", b"important bytes")
        MockS3Handler.corrupt_get = True
        code = replicate.run_once(self.config_path)
        self.assertEqual(code, 1)
        state = json.loads((self.root / "state" / "replication-state.json").read_text())
        self.assertEqual(state["objects"], {})
        report = sorted((self.root / "state").glob("replication-run-*.json"))[-1]
        body = json.loads(report.read_text())
        self.assertIn("error", body["sources"][0])
        self.assertNotIn("important", body["sources"][0].get("error", ""))

    def test_malformed_state_is_refused(self):
        self._dump("20260101T000000Z", b"x")
        state_dir = self.root / "state"
        state_dir.mkdir(parents=True)
        (state_dir / "replication-state.json").write_text('{"version": 99, "objects": {}}')
        with self.assertRaises(replicate.ReplicationError):
            replicate.run_once(self.config_path)


if __name__ == "__main__":
    unittest.main()
