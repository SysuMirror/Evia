import contextlib
import copy
import http.server
import io
import json
import os
from pathlib import Path
import signal
import stat
import sys
import tempfile
import threading
import time
import unittest
from unittest import mock
import zipfile

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "tooling"))
import activity_api as api
import activity_manifest as manifest
import package_assets as packaging

PROJECT = "00000000-0000-4000-8000-000000000001"
TOKEN = "evia_m_never_print_this_credential"


class ManifestTests(unittest.TestCase):
    def setUp(self):
        self.value = json.loads((ROOT / "examples/desired-events.json").read_text())

    def test_example_and_no_predicted_publication_state(self):
        self.assertEqual(manifest.validate(self.value), self.value)
        for key in ("schema_version", "release_revision", "ingestion_ready", "status", "credential"):
            with self.subTest(key=key):
                value = copy.deepcopy(self.value)
                value[key] = True
                with self.assertRaises(api.SafeError):
                    manifest.validate(value)

    def test_paths_duplicates_and_complete_set(self):
        for path in ("../secrets", "/etc/passwd", "src/../../private", "src\\file", "src//file"):
            with self.subTest(path=path):
                value = copy.deepcopy(self.value)
                value["events"][0]["implementation_sites"][0]["path"] = path
                with self.assertRaises(api.SafeError):
                    manifest.validate(value)
        self.value["events"].append(copy.deepcopy(self.value["events"][0]))
        with self.assertRaises(api.SafeError):
            manifest.validate(self.value)
        self.value["events"].pop()
        self.value["complete_release"] = False
        with self.assertRaises(api.SafeError):
            manifest.validate(self.value)

    def test_diff_requires_exact_deletion_declaration(self):
        snapshot = {"project_id": PROJECT, "environment": "development", "active_release_revision": 4,
                    "items": [{"event_key": "old_event", "active": True}, {"event_key": "historical", "active": False}]}
        with self.assertRaises(api.SafeError):
            manifest.membership_diff(self.value, snapshot)
        self.value["removed_event_keys"] = ["old_event"]
        diff = manifest.membership_diff(manifest.validate(self.value), snapshot)
        self.assertEqual(diff["removed"], ["old_event"])
        self.assertEqual(diff["added"], ["item_saved"])
        snapshot["environment"] = "production"
        with self.assertRaises(api.SafeError):
            manifest.membership_diff(self.value, snapshot)

    def test_duplicate_json_keys_and_nonfinite_numbers(self):
        for raw in ('{"a":1,"a":2}', '{"a":NaN}', '{"a":Infinity}', '{"a":1e999}'):
            with self.assertRaises(api.SafeError):
                api.read_json(raw)

    def test_json_schema_stays_in_parity_with_python_validator(self):
        # The published JSON Schema and the operational Python validator encode
        # the same closed sets and limits independently. This guard fails when
        # either side drifts, without pulling a third-party schema library into
        # the stdlib-only shipped tooling.
        schema = json.loads((ROOT / 'schemas' / 'desired-events.schema.json').read_text())
        event_props = schema['properties']['events']['items']['properties']
        self.assertEqual(schema['properties']['format']['const'], 'activity.desired-events.v1')
        self.assertEqual(set(schema['properties']['environment']['enum']),
                         {'development', 'staging', 'production'})
        self.assertEqual(schema['$defs']['event_key']['pattern'], r'^[a-z][a-z0-9_]{0,63}$')
        self.assertEqual(set(event_props['allowed_source_kinds']['items']['enum']),
                         {'browser', 'server'})
        self.assertEqual(set(event_props['fact_kind']['enum']),
                         {'user_intent', 'client_observed', 'server_confirmed'})
        # Numeric bounds must match the hardcoded limits in activity_manifest.validate.
        self.assertEqual(schema['properties']['events']['maxItems'], 200)
        self.assertEqual(event_props['schema']['properties']['properties']['maxProperties'], 50)
        self.assertEqual(event_props['example_properties']['maxProperties'], 50)
        self.assertEqual(event_props['field_metadata']['maxProperties'], 50)
        self.assertEqual(event_props['implementation_sites']['maxItems'], 100)
        self.assertEqual(event_props['display_name']['maxLength'], 128)
        self.assertEqual(event_props['trigger']['maxLength'], 2000)
        self.assertEqual(event_props['implementation_sites']['items']['properties']['path']['maxLength'], 512)
        # The example manifest must satisfy both the schema closed sets and the validator.
        manifest.validate(copy.deepcopy(self.value))
        self.assertEqual(set(self.value['environment'] for _ in [0]), {'development'})
        for event in self.value['events']:
            self.assertTrue(set(event['allowed_source_kinds']) <= {'browser', 'server'})
            self.assertIn(event['fact_kind'], {'user_intent', 'client_observed', 'server_confirmed'})


class PackageTests(unittest.TestCase):
    def test_exact_archive_allowlist_and_deterministic_bytes(self):
        with tempfile.TemporaryDirectory() as temp:
            first, second = Path(temp) / "a.zip", Path(temp) / "b.zip"
            packaging.package(ROOT, first)
            packaging.package(ROOT, second)
            self.assertEqual(first.read_bytes(), second.read_bytes())
            with zipfile.ZipFile(first) as archive:
                self.assertEqual(archive.namelist(), list(packaging.ALLOWLIST))
                self.assertFalse(any(".agents" in p or "AGENTS.md" in p or ".env" in p for p in archive.namelist()))
            with self.assertRaises(FileExistsError):
                packaging.package(ROOT, first)

    def test_rejects_malicious_paths_and_symlinks(self):
        for name in ("../AGENTS.md", "/etc/passwd", ".agents/skills/a", ".env", "AGENTS.md", "a/../b", "a\\b"):
            with self.subTest(name=name), self.assertRaises(ValueError):
                packaging.collect(ROOT, (name,))
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / "file").symlink_to(ROOT / "README.md")
            with self.assertRaises(ValueError):
                packaging.collect(root, ("file",))
            (root / "folder").symlink_to(ROOT / "tooling", target_is_directory=True)
            with self.assertRaises(ValueError):
                packaging.collect(root, ("folder/activity_api.py",))
            os.mkfifo(root / "fifo")
            with self.assertRaises(ValueError):
                packaging.collect(root, ("fifo",))


class HTTPTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        class Handler(http.server.BaseHTTPRequestHandler):
            mode = "ok"
            calls = []

            def log_message(self, *args):
                pass

            def do_GET(self):
                self.calls.append((self.path, self.headers.get("Authorization"), self.headers.get("Cookie")))
                if self.mode == "bad_status":
                    self.connection.sendall((TOKEN + "\r\n\r\n").encode())
                    self.close_connection = True
                    return
                if self.mode == "slow_headers":
                    self.server.release.wait(2)
                    self.close_connection = True
                    return
                if self.mode == "trickle":
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.end_headers()
                    try:
                        while not self.server.release.is_set():
                            self.wfile.write(b" ")
                            self.wfile.flush()
                            self.server.release.wait(0.01)
                    except (BrokenPipeError, ConnectionResetError):
                        pass
                    self.close_connection = True
                    return
                if self.mode == "redirect":
                    self.send_response(302)
                    self.send_header("Location", "/credential-exfiltration")
                    self.end_headers()
                    return
                if self.mode == "error":
                    self.send_response(401)
                    self.send_header("Content-Type", "application/json")
                    self.end_headers()
                    self.wfile.write(json.dumps({"message": TOKEN}).encode())
                    return
                body = {"principal_id": PROJECT, "credential_id": PROJECT, "project_id": PROJECT,
                        "environment": "production" if self.mode == "wrong_scope" else "development",
                        "name": "test", "permissions": ["project.read"], "expires_at": "2030-01-01T00:00:00Z"}
                raw = json.dumps(body).encode() if self.mode != "malformed" else b'{"project_id":'
                if self.mode == "oversized":
                    raw = b' ' * (api.MAX_RESPONSE + 1)
                if self.mode == "expired":
                    body["expires_at"] = "2020-01-01T00:00:00Z"
                    raw = json.dumps(body).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(raw)
        cls.handler = Handler
        cls.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        cls.server.release = threading.Event()
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()
        cls.origin = f"http://127.0.0.1:{cls.server.server_port}"

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()
        cls.thread.join()

    def setUp(self):
        self.handler.mode = "ok"
        self.handler.calls.clear()
        self.server.release.clear()

    def tearDown(self):
        self.server.release.set()

    def run_helper(self, *extra):
        stdout, stderr = io.StringIO(), io.StringIO()
        with mock.patch.dict(os.environ, {"EVIA_MACHINE_TOKEN": TOKEN}), contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            result = api.main(["--endpoint", self.origin, "--allow-loopback-http", "--project", PROJECT,
                               "--environment", "development", *extra])
        self.assertNotIn(TOKEN, stdout.getvalue() + stderr.getvalue())
        return result, stdout.getvalue(), stderr.getvalue()

    def test_discovery_private_output_and_no_cookie(self):
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / "identity.json"
            result, stdout, _ = self.run_helper("--output", str(output))
            self.assertEqual(result, 0)
            self.assertNotIn("principal_id", stdout)
            self.assertEqual(stat.S_IMODE(output.stat().st_mode), 0o600)
            self.assertEqual(self.handler.calls, [("/api/v1/machine-identity", "Bearer " + TOKEN, None)])
            self.assertEqual(self.run_helper("--output", str(output))[0], 1)

    def test_redirect_error_malformed_and_scope_fail_closed(self):
        for mode in ("redirect", "error", "malformed", "wrong_scope", "oversized", "expired", "bad_status"):
            with self.subTest(mode=mode):
                self.handler.calls.clear()
                self.handler.mode = mode
                result, _, _ = self.run_helper("--path", f"/api/v1/projects/{PROJECT}")
                self.assertEqual(result, 1)
                self.assertEqual(len(self.handler.calls), 1)
                self.assertEqual(self.handler.calls[0][0], "/api/v1/machine-identity")

    def test_absolute_deadline_covers_headers_and_continuously_trickled_body(self):
        for mode in ("slow_headers", "trickle"):
            with self.subTest(mode=mode):
                self.handler.mode = mode
                self.server.release.clear()
                handler = signal.getsignal(signal.SIGALRM)
                started = time.monotonic()
                try:
                    with self.assertRaises(api.SafeError):
                        api.call(self.origin, TOKEN, "GET", "/api/v1/machine-identity", timeout=0.08)
                    self.assertLess(time.monotonic() - started, 0.6)
                    self.assertEqual(signal.getsignal(signal.SIGALRM), handler)
                    self.assertEqual(signal.getitimer(signal.ITIMER_REAL), (0.0, 0.0))
                finally:
                    self.server.release.set()

    def test_authentication_error_reports_only_numeric_status(self):
        self.handler.mode = "error"
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / "identity.json"
            status, stdout, stderr = self.run_helper("--output", str(output))
            self.assertEqual(status, 1)
            self.assertEqual(stdout, "")
            self.assertEqual(stderr, "Request failed (HTTP 401); no response body printed.\n")
            self.assertFalse(output.exists())

    def test_default_proxy_handling_is_preserved(self):
        original = api.request.build_opener
        with mock.patch.object(api.request, "build_opener", wraps=original) as build:
            self.assertEqual(self.run_helper()[0], 0)
            args = build.call_args.args
            self.assertEqual(len(args), 1)
            self.assertIsInstance(args[0], api.NoRedirect)

    def test_existing_alarm_and_non_main_thread_fail_closed(self):
        handler = signal.getsignal(signal.SIGALRM)
        try:
            signal.setitimer(signal.ITIMER_REAL, 5)
            with self.assertRaises(api.SafeError):
                with api.deadline(1):
                    self.fail("must not replace an active timer")
            self.assertGreater(signal.getitimer(signal.ITIMER_REAL)[0], 4)
            self.assertEqual(signal.getsignal(signal.SIGALRM), handler)
        finally:
            signal.setitimer(signal.ITIMER_REAL, 0)
        result = []
        def worker():
            try:
                with api.deadline(1):
                    result.append("unexpected")
            except api.SafeError:
                result.append("rejected")
        thread = threading.Thread(target=worker)
        thread.start()
        thread.join(1)
        self.assertFalse(thread.is_alive())
        self.assertEqual(result, ["rejected"])

    def test_scheme_path_and_credential_file_rules(self):
        for value in ("http://events.example", "https://user:pass@events.example", "https://events.example/path", "https://events.example?token=x"):
            with self.assertRaises(api.SafeError):
                api.endpoint(value, True)
        for path in ("https://other.example/", f"/api/v1/projects/{PROJECT}/../sources", f"/api/v1/projects/{PROJECT}/sources%2fsecret", f"/api/v1/projects/{PROJECT}/sources?environment=production", f"/api/v1/projects/{PROJECT}/sources?environment=development&environment=development"):
            with self.assertRaises(api.SafeError):
                api.scoped_path(path, PROJECT, "development")
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "token"
            path.write_text(TOKEN)
            path.chmod(0o600)
            self.assertEqual(api.private_read(path), TOKEN)
            link = Path(temp) / "link"
            link.symlink_to(path)
            with self.assertRaises(api.SafeError):
                api.private_read(link)
            path.chmod(0o644)
            with self.assertRaises(api.SafeError):
                api.private_read(path)
            fifo = Path(temp) / "fifo"
            os.mkfifo(fifo, 0o600)
            with self.assertRaises(api.SafeError):
                api.private_read(fifo)

    def test_metric_routes_preserve_scope_and_explicit_allowlist(self):
        base = f"/api/v1/projects/{PROJECT}/metrics"
        for suffix in ("?environment=development&limit=20", f"/{PROJECT}",
                       f"/{PROJECT}/draft", f"/{PROJECT}/validations",
                       f"/{PROJECT}/versions", f"/{PROJECT}/versions/1",
                       f"/{PROJECT}/versions/1/queries"):
            with self.subTest(suffix=suffix):
                self.assertEqual(api.scoped_path(base + suffix, PROJECT, "development"), base + suffix)
        for path in (base + "/latest", base + f"/{PROJECT}/versions/0",
                     base + f"/{PROJECT}/versions/1/sql", base + f"/{PROJECT}/archive",
                     base + "?environment=production", base + "?permission=metrics.write",
                     base.replace(PROJECT, "00000000-0000-4000-8000-000000000002")):
            with self.subTest(path=path), self.assertRaises(api.SafeError):
                api.scoped_path(path, PROJECT, "development")

    def test_formula_metric_routes_preserve_scope_and_explicit_allowlist(self):
        base = f"/api/v1/projects/{PROJECT}/formula-metrics"
        for suffix in ("?environment=development&limit=20", f"/{PROJECT}",
                       f"/{PROJECT}/draft", f"/{PROJECT}/validations",
                       f"/{PROJECT}/versions", f"/{PROJECT}/versions/1",
                       f"/{PROJECT}/versions/1/queries"):
            with self.subTest(suffix=suffix):
                self.assertEqual(api.scoped_path(base + suffix, PROJECT, "development"), base + suffix)
        for path in (base + "/latest", base + f"/{PROJECT}/versions/0",
                     base + f"/{PROJECT}/versions/1/sql",
                     base + f"/{PROJECT}/archive", base + f"/{PROJECT}/unarchive",
                     base + "?environment=production", base + "?permission=metrics.write",
                     base.replace(PROJECT, "00000000-0000-4000-8000-000000000002")):
            with self.subTest(path=path), self.assertRaises(api.SafeError):
                api.scoped_path(path, PROJECT, "development")


if __name__ == "__main__":
    unittest.main()
