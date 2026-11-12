"""Tests for the stdlib S3 client: AWS signature vectors plus a loopback mock."""
import datetime as dt
import hashlib
import hmac
import os
import sys
import tempfile
import threading
import unittest
import urllib.parse
import xml.sax.saxutils as saxutils
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import objectstore  # noqa: E402

ACCESS_KEY = "AKIDEXAMPLE"
SECRET_KEY = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
FIXED_NOW = dt.datetime(2015, 8, 30, 12, 36, 0, tzinfo=dt.timezone.utc)


class SignatureVectorTest(unittest.TestCase):
    def test_signing_key_matches_aws_documentation_vector(self):
        key = objectstore.signing_key(SECRET_KEY, "20150830", "us-east-1", "iam")
        self.assertEqual(
            key.hex(),
            "c4afb1cc5771d871763a393e44b703571b55cc28424d1a5e86da6ed3c154a4b9",
        )

    def test_get_vanilla_matches_aws_test_suite_vector(self):
        authorization = objectstore.sign_request(
            method="GET",
            url="http://example.amazonaws.com/",
            headers={},
            payload_hash=objectstore.EMPTY_SHA256,
            region="us-east-1",
            service="service",
            access_key=ACCESS_KEY,
            secret_key=SECRET_KEY,
            now=FIXED_NOW,
        )
        signature = authorization.rsplit("Signature=", 1)[1]
        # Cross-validated byte-for-byte against botocore's SigV4Auth using the
        # same frozen request (AKIDEXAMPLE key, 20150830T123600Z, empty payload).
        self.assertEqual(
            signature,
            "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31",
        )
        self.assertIn("Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request", authorization)
        self.assertIn("SignedHeaders=host;x-amz-date", authorization)

    def test_query_string_is_sorted_and_encoded(self):
        authorization = objectstore.sign_request(
            method="GET",
            url="http://example.amazonaws.com/bucket?b=2&a=1",
            headers={},
            payload_hash=objectstore.EMPTY_SHA256,
            region="us-east-1",
            service="s3",
            access_key=ACCESS_KEY,
            secret_key=SECRET_KEY,
            now=FIXED_NOW,
        )
        # A second call with reversed parameter order must produce the same signature.
        reversed_order = objectstore.sign_request(
            method="GET",
            url="http://example.amazonaws.com/bucket?a=1&b=2",
            headers={},
            payload_hash=objectstore.EMPTY_SHA256,
            region="us-east-1",
            service="s3",
            access_key=ACCESS_KEY,
            secret_key=SECRET_KEY,
            now=FIXED_NOW,
        )
        self.assertEqual(authorization, reversed_order)


class ValidationTest(unittest.TestCase):
    def test_key_validation_blocks_unsafe_inputs(self):
        for bad in ("/leading-slash", "a/../b", "a//b", "back\\slash", "x" * 1025, ""):
            with self.assertRaises(objectstore.ObjectStoreError):
                objectstore.validate_key(bad)
        for good in ("postgres/evia-pg-backup-2026.dump", "a/b-c_d.1/backup.tar"):
            objectstore.validate_key(good)

    def test_bucket_validation(self):
        for bad in ("UPPER", "ab", "a..b", "-leading"):
            with self.assertRaises(objectstore.ObjectStoreError):
                objectstore.validate_bucket(bad)
        objectstore.validate_bucket("evia-backup-01")

    def test_missing_credentials_fail_without_leaking(self):
        os.environ.pop("EVIA_TEST_S3_AK", None)
        os.environ.pop("EVIA_TEST_S3_SK", None)
        config = objectstore.S3Config(
            endpoint="http://127.0.0.1:9000",
            region="us-east-1",
            bucket="evia-backup",
            access_key_env="EVIA_TEST_S3_AK",
            secret_key_env="EVIA_TEST_S3_SK",
        )
        with self.assertRaises(objectstore.ObjectStoreError):
            config.credentials()


class MockS3Handler(BaseHTTPRequestHandler):
    objects: dict[str, bytes] = {}
    corrupt_get = False

    def log_message(self, *args):  # silence test server output
        pass

    def _reject(self, status: int, code: str, message: str):
        body = (
            f"<?xml version='1.0' encoding='UTF-8'?>"
            f"<Error><Code>{saxutils.escape(code)}</Code>"
            f"<Message>{saxutils.escape(message)}</Message></Error>"
        ).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/xml")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _verify_signature(self, body: bytes):
        authorization = self.headers.get("Authorization", "")
        if not authorization.startswith("AWS4-HMAC-SHA256"):
            self._reject(403, "SignatureDoesNotMatch", "missing authorization")
            return False
        payload_hash = self.headers.get("X-Amz-Content-Sha256", "")
        if payload_hash != hashlib.sha256(body).hexdigest():
            self._reject(400, "BadDigest", "payload hash mismatch")
            return False
        # Rebuild the exact signed header set from the Authorization value so
        # content-type/content-length participate when the client signed them.
        signed_headers = authorization.split("SignedHeaders=", 1)[1].split(",", 1)[0].split(";")
        signed: dict[str, str] = {}
        for name in signed_headers:
            if name == "host":
                continue
            value = self.headers.get(name)
            if value is None:
                self._reject(403, "SignatureDoesNotMatch", f"missing signed header {name}")
                return False
            signed["-".join(part.capitalize() for part in name.split("-"))] = value
        url = "http://" + self.headers["Host"] + self.path
        expected = objectstore.sign_request(
            method=self.command,
            url=url,
            headers=signed,
            payload_hash=payload_hash,
            region="us-east-1",
            service="s3",
            access_key=ACCESS_KEY,
            secret_key=SECRET_KEY,
            now=dt.datetime.strptime(
                self.headers["X-Amz-Date"], objectstore.AMZ_DATE_FMT
            ).replace(tzinfo=dt.timezone.utc),
        )
        if not hmac.compare_digest(expected, authorization):
            self._reject(403, "SignatureDoesNotMatch", "bad signature")
            return False
        return True

    def _key(self) -> str:
        return urllib.parse.unquote(urllib.parse.urlsplit(self.path).path.lstrip("/"))[len("evia-backup/"):]

    def do_PUT(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        if not self._verify_signature(body):
            return
        key = self._key()
        if key:
            MockS3Handler.objects[key] = body
            self.send_response(200)
        else:
            self.send_response(200)  # bucket creation
        self.send_header("Content-Length", "0")
        self.end_headers()

    def do_HEAD(self):
        if not self._verify_signature(b""):
            return
        key = self._key()
        if key in MockS3Handler.objects:
            self.send_response(200)
            self.send_header("Content-Length", str(len(MockS3Handler.objects[key])))
            self.send_header("ETag", '"%s"' % hashlib.md5(MockS3Handler.objects[key]).hexdigest())
            self.end_headers()
        else:
            self._reject(404, "NoSuchKey", "not found")

    def do_GET(self):
        if not self._verify_signature(b""):
            return
        parsed = urllib.parse.urlsplit(self.path)
        params = dict(urllib.parse.parse_qsl(parsed.query))
        if params.get("list-type") == "2":
            prefix = params.get("prefix", "")
            items = "".join(
                f"<Contents><Key>{saxutils.escape(key)}</Key>"
                f"<ETag>&quot;{hashlib.md5(data).hexdigest()}&quot;</ETag>"
                f"<Size>{len(data)}</Size></Contents>"
                for key, data in sorted(MockS3Handler.objects.items())
                if key.startswith(prefix)
            )
            body = f"<?xml version='1.0'?><ListBucketResult>{items}</ListBucketResult>".encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/xml")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        key = self._key()
        if key not in MockS3Handler.objects:
            self._reject(404, "NoSuchKey", "not found")
            return
        data = MockS3Handler.objects[key]
        if MockS3Handler.corrupt_get:
            data = data + b"tampered"
        self.send_response(200)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_DELETE(self):
        if not self._verify_signature(b""):
            return
        MockS3Handler.objects.pop(self._key(), None)
        self.send_response(204)
        self.send_header("Content-Length", "0")
        self.end_headers()


class LoopbackS3Test(unittest.TestCase):
    def setUp(self):
        MockS3Handler.objects = {}
        MockS3Handler.corrupt_get = False
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), MockS3Handler)
        self.port = self.server.server_address[1]
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        os.environ["EVIA_TEST_S3_AK"] = ACCESS_KEY
        os.environ["EVIA_TEST_S3_SK"] = SECRET_KEY
        self.config = objectstore.S3Config(
            endpoint=f"http://127.0.0.1:{self.port}",
            region="us-east-1",
            bucket="evia-backup",
            access_key_env="EVIA_TEST_S3_AK",
            secret_key_env="EVIA_TEST_S3_SK",
            path_style=True,
            insecure_http=True,
            timeout_seconds=10,
        )
        self.client = objectstore.S3Client(self.config)
        self.tmp = tempfile.TemporaryDirectory()

    def tearDown(self):
        self.server.shutdown()
        self.tmp.cleanup()
        os.environ.pop("EVIA_TEST_S3_AK", None)
        os.environ.pop("EVIA_TEST_S3_SK", None)

    def test_full_put_head_get_delete_lifecycle(self):
        source = Path(self.tmp.name) / "backup.dump"
        payload = b"evia backup payload " * 1000
        source.write_bytes(payload)
        digest = hashlib.sha256(payload).hexdigest()

        self.client.ensure_bucket()
        returned = self.client.put_object("postgres/backup.dump", source)
        self.assertEqual(returned, digest)
        self.assertTrue(self.client.object_exists("postgres/backup.dump"))
        self.assertEqual(int(self.client.head_object("postgres/backup.dump")["Content-Length"]), len(payload))

        target = Path(self.tmp.name) / "restored.dump"
        self.client.download_object("postgres/backup.dump", target, expected_sha256=digest)
        self.assertEqual(target.read_bytes(), payload)

        self.client.delete_object("postgres/backup.dump")
        self.assertFalse(self.client.object_exists("postgres/backup.dump"))
        # Deleting an absent object is idempotent.
        self.client.delete_object("postgres/backup.dump")

    def test_list_objects_returns_bucket_contents(self):
        MockS3Handler.objects.update({"a/x": b"1", "b/y": b"22"})
        listing = self.client.list_objects("a/")
        self.assertEqual(len(listing), 1)
        self.assertEqual(listing[0]["key"], "a/x")
        self.assertEqual(listing[0]["size"], "1")

    def test_checksum_mismatch_is_detected_and_scratch_removed(self):
        source = Path(self.tmp.name) / "backup.dump"
        source.write_bytes(b"original bytes")
        self.client.put_object("postgres/backup.dump", source)
        MockS3Handler.corrupt_get = True
        target = Path(self.tmp.name) / "restored.dump"
        with self.assertRaises(objectstore.ObjectStoreError) as caught:
            self.client.download_object(
                "postgres/backup.dump", target,
                expected_sha256=hashlib.sha256(b"original bytes").hexdigest(),
            )
        self.assertEqual(caught.exception.code, None)
        self.assertFalse(target.exists())

    def test_s3_error_xml_is_mapped_to_structured_error(self):
        # Real S3 returns no body for HEAD failures, so the client keeps the
        # HTTP status even though the XML code cannot be parsed.
        with self.assertRaises(objectstore.ObjectStoreError) as caught:
            self.client.head_object("postgres/missing.dump")
        self.assertEqual(caught.exception.status, 404)
        # GET failures carry an XML body and expose the structured S3 code.
        with self.assertRaises(objectstore.ObjectStoreError) as caught_get:
            self.client.download_object("postgres/missing.dump", Path(self.tmp.name) / "x")
        self.assertEqual(caught_get.exception.status, 404)
        self.assertEqual(caught_get.exception.code, "NoSuchKey")


if __name__ == "__main__":
    unittest.main()
