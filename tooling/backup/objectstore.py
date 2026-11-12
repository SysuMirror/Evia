"""Minimal S3-compatible object store client for offsite backup replication.

The deployment must be able to copy verified backup artifacts into an S3
compatible bucket (AWS S3, MinIO, Ceph RGW) without pulling a third-party SDK
into the recovery toolchain: a disaster recovery tool should keep its
dependency surface as small as possible. This module therefore implements
only the operations the replication runner needs, on top of the Python
standard library:

* AWS Signature Version 4 (``AWS4-HMAC-SHA256``), path-style by default so
  MinIO and Ceph work without DNS wildcard support.
* Streaming upload/download with an explicit sha256 payload hash, object
  existence checks, deletion and ListObjectsV2.
* Bounded key validation so a manifest can never address ``..`` segments or
  absolute paths, and structured errors carrying the S3 error code.

Credentials are read from named environment variables; they never appear in
argv, exception text or replication reports.
"""
from __future__ import annotations

import dataclasses
import datetime as dt
import hashlib
import hmac
import os
import re
import urllib.error
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET
from pathlib import Path

UNSIGNED_PAYLOAD = "UNSIGNED-PAYLOAD"
EMPTY_SHA256 = hashlib.sha256(b"").hexdigest()
KEY_PATTERN = re.compile(r"^[A-Za-z0-9._/-]{1,1024}$")
BUCKET_PATTERN = re.compile(r"^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$")
AMZ_DATE_FMT = "%Y%m%dT%H%M%SZ"
DATE_FMT = "%Y%m%d"
CHUNK_SIZE = 1024 * 1024


class ObjectStoreError(RuntimeError):
    """All transport/protocol failures are normalized to this type."""

    def __init__(self, message: str, status: int | None = None, code: str | None = None):
        super().__init__(message)
        self.status = status
        self.code = code


@dataclasses.dataclass(frozen=True)
class S3Config:
    endpoint: str
    region: str
    bucket: str
    access_key_env: str
    secret_key_env: str
    path_style: bool = True
    insecure_http: bool = False
    timeout_seconds: int = 60

    def credentials(self) -> tuple[str, str]:
        access = os.environ.get(self.access_key_env, "")
        secret = os.environ.get(self.secret_key_env, "")
        if not access or not secret:
            raise ObjectStoreError(
                f"missing S3 credentials in {self.access_key_env}/{self.secret_key_env}"
            )
        return access, secret


def validate_bucket(name: str) -> None:
    if not BUCKET_PATTERN.fullmatch(name) or ".." in name:
        raise ObjectStoreError(f"invalid S3 bucket name: {name!r}")


def validate_key(key: str) -> None:
    if not KEY_PATTERN.fullmatch(key) or key.startswith("/") or "//" in key or ".." in key.split("/"):
        raise ObjectStoreError(f"invalid S3 object key: {key!r}")


def _uri_encode(text: str, *, encode_slash: bool = False) -> str:
    # RFC 3986 unreserved characters only; every other byte is percent-encoded.
    safe = "-_.~" + ("/" if not encode_slash else "")
    return urllib.parse.quote(text, safe=safe)


def signing_key(secret: str, date_stamp: str, region: str, service: str) -> bytes:
    """Derive the SigV4 signing key; validated against the AWS test vector."""
    k_date = hmac.new(("AWS4" + secret).encode(), date_stamp.encode(), hashlib.sha256).digest()
    k_region = hmac.new(k_date, region.encode(), hashlib.sha256).digest()
    k_service = hmac.new(k_region, service.encode(), hashlib.sha256).digest()
    return hmac.new(k_service, b"aws4_request", hashlib.sha256).digest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(CHUNK_SIZE), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _parse_s3_error(body: bytes, status: int) -> ObjectStoreError:
    code = "S3_ERROR"
    message = f"object store returned HTTP {status}"
    try:
        root = ET.fromstring(body)
        code_node = root.find("Code")
        message_node = root.find("Message")
        if code_node is not None and code_node.text:
            code = code_node.text
        if message_node is not None and message_node.text:
            message = message_node.text
    except ET.ParseError:
        pass
    return ObjectStoreError(f"{code}: {message}", status=status, code=code)


def sign_request(
    *,
    method: str,
    url: str,
    headers: dict[str, str],
    payload_hash: str,
    region: str,
    service: str,
    access_key: str,
    secret_key: str,
    now: dt.datetime,
) -> str:
    """Build the Authorization header for one signed HTTP request."""
    parsed = urllib.parse.urlsplit(url)
    amz_date = now.strftime(AMZ_DATE_FMT)
    date_stamp = now.strftime(DATE_FMT)
    headers = dict(headers)
    headers["Host"] = parsed.netloc
    headers["X-Amz-Date"] = amz_date

    canonical_query = ""
    if parsed.query:
        pairs = urllib.parse.parse_qsl(parsed.query, keep_blank_values=True)
        canonical_query = "&".join(
            f"{_uri_encode(k, encode_slash=True)}={_uri_encode(v, encode_slash=True)}"
            for k, v in sorted(pairs)
        )
    canonical_headers = "".join(
        f"{name.lower()}:{headers[name].strip()}\n" for name in sorted(headers)
    )
    signed_headers = ";".join(name.lower() for name in sorted(headers))
    canonical_request = "\n".join(
        [
            method,
            _uri_encode(urllib.parse.unquote(parsed.path)),
            canonical_query,
            canonical_headers,
            signed_headers,
            payload_hash,
        ]
    )
    scope = f"{date_stamp}/{region}/{service}/aws4_request"
    string_to_sign = "\n".join(
        [
            "AWS4-HMAC-SHA256",
            amz_date,
            scope,
            hashlib.sha256(canonical_request.encode()).hexdigest(),
        ]
    )
    signature = hmac.new(
        signing_key(secret_key, date_stamp, region, service),
        string_to_sign.encode(),
        hashlib.sha256,
    ).hexdigest()
    return (
        f"AWS4-HMAC-SHA256 Credential={access_key}/{scope}, "
        f"SignedHeaders={signed_headers}, Signature={signature}"
    )


class S3Client:
    """Small signed HTTP client; one instance maps to one endpoint/bucket."""

    def __init__(self, config: S3Config):
        validate_bucket(config.bucket)
        self.config = config
        self._opener = urllib.request.build_opener()

    # -- URL / signing ------------------------------------------------------

    def _host(self) -> str:
        parsed = urllib.parse.urlsplit(self.config.endpoint)
        if parsed.scheme not in ("http", "https"):
            raise ObjectStoreError("endpoint must use http(s)")
        if self.config.insecure_http and parsed.scheme != "http":
            raise ObjectStoreError("insecure_http requires an http endpoint")
        if not parsed.hostname:
            raise ObjectStoreError("endpoint requires a hostname")
        return parsed.netloc

    def _url(self, key: str = "", query: dict[str, str] | None = None) -> str:
        parsed = urllib.parse.urlsplit(self.config.endpoint)
        base = f"{parsed.scheme}://" + parsed.netloc.rstrip("/")
        if self.config.path_style:
            base += "/" + self.config.bucket
        if key:
            base += "/" + _uri_encode(key)
        if query:
            base += "?" + urllib.parse.urlencode(sorted(query.items()))
        return base

    def _sign(
        self,
        method: str,
        url: str,
        headers: dict[str, str],
        payload_hash: str,
        now: dt.datetime,
    ) -> str:
        access, secret = self.config.credentials()
        return sign_request(
            method=method,
            url=url,
            headers=dict(headers),
            payload_hash=payload_hash,
            region=self.config.region,
            service="s3",
            access_key=access,
            secret_key=secret,
            now=now,
        )

    def _request(
        self,
        method: str,
        url: str,
        *,
        body: bytes | None = None,
        extra_headers: dict[str, str] | None = None,
        payload_hash: str | None = None,
    ) -> urllib.request.Response:
        headers = dict(extra_headers or {})
        if payload_hash is None:
            payload_hash = hashlib.sha256(body or b"").hexdigest()
        now = dt.datetime.now(dt.timezone.utc)
        headers["X-Amz-Date"] = now.strftime(AMZ_DATE_FMT)
        headers["X-Amz-Content-Sha256"] = payload_hash
        authorization = self._sign(method, url, headers, payload_hash, now)
        headers["Authorization"] = authorization
        request = urllib.request.Request(url, data=body, method=method, headers=headers)
        try:
            return self._opener.open(request, timeout=self.config.timeout_seconds)
        except urllib.error.HTTPError as error:
            raise _parse_s3_error(error.read(), error.code) from error
        except urllib.error.URLError as error:
            raise ObjectStoreError(f"cannot reach object store endpoint: {error.reason}") from error

    # -- Public operations --------------------------------------------------

    def bucket_exists(self) -> bool:
        try:
            self._request("HEAD", self._url())
            return True
        except ObjectStoreError as error:
            if error.status == 404:
                return False
            raise

    def ensure_bucket(self) -> None:
        if self.bucket_exists():
            return
        self._request("PUT", self._url())

    def put_object(self, key: str, path: Path, content_type: str = "application/octet-stream") -> str:
        validate_key(key)
        if not path.is_file():
            raise ObjectStoreError(f"local artifact not found: {path}")
        payload_hash = sha256_file(path)
        data = path.read_bytes()
        headers = {"Content-Type": content_type, "Content-Length": str(len(data))}
        self._request(
            "PUT",
            self._url(key),
            body=data,
            extra_headers=headers,
            payload_hash=payload_hash,
        )
        return payload_hash

    def head_object(self, key: str) -> dict[str, str]:
        validate_key(key)
        response = self._request("HEAD", self._url(key))
        return dict(response.headers.items())

    def object_exists(self, key: str) -> bool:
        try:
            self.head_object(key)
            return True
        except ObjectStoreError as error:
            if error.status == 404:
                return False
            raise

    def download_object(self, key: str, destination: Path, expected_sha256: str | None = None) -> None:
        validate_key(key)
        response = self._request("GET", self._url(key))
        digest = hashlib.sha256()
        destination.parent.mkdir(parents=True, exist_ok=True)
        tmp = destination.with_name(destination.name + ".tmp")
        with tmp.open("wb") as handle:
            while True:
                chunk = response.read(CHUNK_SIZE)
                if not chunk:
                    break
                digest.update(chunk)
                handle.write(chunk)
        actual = digest.hexdigest()
        if expected_sha256 and not hmac.compare_digest(actual, expected_sha256):
            tmp.unlink(missing_ok=True)
            raise ObjectStoreError(
                f"downloaded object {key!r} checksum mismatch: expected {expected_sha256}, got {actual}"
            )
        os.replace(tmp, destination)

    def delete_object(self, key: str) -> None:
        validate_key(key)
        try:
            self._request("DELETE", self._url(key))
        except ObjectStoreError as error:
            if error.status != 404:
                raise

    def list_objects(self, prefix: str = "") -> list[dict[str, str]]:
        query = {"list-type": "2"}
        if prefix:
            query["prefix"] = prefix
        response = self._request("GET", self._url(query=query))
        root = ET.fromstring(response.read())
        namespace = ""
        if root.tag.startswith("{"):
            namespace = root.tag.split("}", 1)[0].strip("{}")
        tag = f"{{{namespace}}}Contents" if namespace else "Contents"
        objects: list[dict[str, str]] = []
        for contents in root.iter(tag):
            key_node = contents.find(f"{{{namespace}}}Key" if namespace else "Key")
            etag_node = contents.find(f"{{{namespace}}}ETag" if namespace else "ETag")
            size_node = contents.find(f"{{{namespace}}}Size" if namespace else "Size")
            if key_node is None or not key_node.text:
                continue
            objects.append(
                {
                    "key": key_node.text,
                    "etag": etag_node.text if etag_node is not None and etag_node.text else "",
                    "size": size_node.text if size_node is not None and size_node.text else "0",
                }
            )
        return objects
