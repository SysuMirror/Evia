#!/usr/bin/env python3
"""Bounded, scoped requests to the documented Evia management API."""
import argparse
from contextlib import contextmanager
from datetime import datetime, timezone
from http.client import HTTPException
import ipaddress
import json
import math
import os
import re
import signal
import stat
import sys
import threading
from urllib import error, parse, request

MAX_BODY = 1024 * 1024
MAX_RESPONSE = 2 * 1024 * 1024
REQUEST_DEADLINE = 20
UUID = r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"
ENVIRONMENTS = {"development", "staging", "production"}


class SafeError(Exception):
    """Messages are fixed strings, never untrusted response/credential content."""


class HTTPStatusError(SafeError):
    def __init__(self, status):
        self.status = status if type(status) is int and 100 <= status <= 599 else None
        super().__init__("HTTP request failed")


def read_json(raw):
    def finite(value):
        number = float(value)
        if not math.isfinite(number):
            raise SafeError("non-finite JSON number")
        return number
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise SafeError("duplicate JSON key")
            result[key] = value
        return result
    try:
        return json.loads(raw, object_pairs_hook=pairs, parse_float=finite,
                          parse_constant=lambda _: (_ for _ in ()).throw(SafeError("non-finite JSON number")))
    except (ValueError, UnicodeError, RecursionError):
        raise SafeError("invalid JSON") from None


def private_read(path):
    try:
        flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_NONBLOCK", 0)
        fd = os.open(path, flags)
        with os.fdopen(fd, "rb") as handle:
            info = os.fstat(handle.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077 or info.st_uid != os.getuid():
                raise SafeError("credential file must be an owned private regular file")
            raw = handle.read(1025)
        if len(raw) > 1024:
            raise SafeError("credential file too large")
        return raw.decode("ascii").strip()
    except (OSError, UnicodeError):
        raise SafeError("cannot read private credential file") from None


def credential(env_name, filename):
    token = private_read(filename) if filename else os.environ.get(env_name, "")
    if not re.fullmatch(r"evia_m_[A-Za-z0-9_-]{1,500}", token):
        raise SafeError("a valid machine credential is required")
    return token


def endpoint(value, allow_loopback):
    try:
        url = parse.urlsplit(value)
        host = url.hostname
        _ = url.port
        if not host or url.username or url.password or url.query or url.fragment or url.path not in ("", "/"):
            raise SafeError("endpoint must be a plain origin")
        loopback = host == "localhost"
        try:
            loopback = loopback or ipaddress.ip_address(host).is_loopback
        except ValueError:
            pass
        if url.scheme != "https" and not (url.scheme == "http" and allow_loopback and loopback):
            raise SafeError("HTTPS required; HTTP needs explicit loopback development mode")
        return value.rstrip("/")
    except ValueError:
        raise SafeError("invalid endpoint") from None


def scoped_path(value, project, environment):
    if not re.fullmatch(UUID, project) or environment not in ENVIRONMENTS:
        raise SafeError("explicit project UUID and environment required")
    if len(value) > 8192 or any(c in value for c in ("%", "\\", "#")):
        raise SafeError("invalid API path")
    parsed = parse.urlsplit(value)
    prefix = f"/api/v1/projects/{project}"
    if parsed.scheme or parsed.netloc or not (parsed.path == prefix or parsed.path.startswith(prefix + "/")):
        raise SafeError("API path must match the explicit project")
    relative = parsed.path[len(prefix):]
    patterns = [r"", r"/sources(?:/" + UUID + r"(?:/credential-rotations)?)?",
                r"/event-definitions(?:/[a-z][a-z0-9_]{0,63}/(?:draft|validate|versions/[1-9][0-9]*))?",
                r"/releases(?:/" + UUID + r")?", r"/release-activations",
                r"/analysis-(?:capabilities|catalog)", r"/query-validations",
                r"/metrics(?:/" + UUID + r"(?:/(?:draft|validations|versions(?:/[1-9][0-9]*(?:/queries)?)?))?)?",
                r"/formula-metrics(?:/" + UUID + r"(?:/(?:draft|validations|versions(?:/[1-9][0-9]*(?:/queries)?)?))?)?",
                r"/queries(?:/" + UUID + r"(?:/result)?)?", r"/journeys/query"]
    if not any(re.fullmatch(pattern, relative) for pattern in patterns):
        raise SafeError("API path outside the integration helper allowlist")
    values = parse.parse_qs(parsed.query, keep_blank_values=True)
    if any(len(v) != 1 for v in values.values()) or set(values) - {"environment", "cursor", "limit"}:
        raise SafeError("invalid API query")
    if "environment" in values and values["environment"] != [environment]:
        raise SafeError("API query environment differs from explicit scope")
    return value


class NoRedirect(request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise SafeError("redirect refused")


@contextmanager
def deadline(seconds):
    # SIGALRM bounds DNS, connection, headers and body reads in this POSIX CLI.
    # Refuse an active caller timer instead of delaying another owner's alarm.
    if (threading.current_thread() is not threading.main_thread()
            or not all(hasattr(signal, attr) for attr in ("SIGALRM", "ITIMER_REAL", "setitimer", "getitimer"))
            or not isinstance(seconds, (int, float)) or not math.isfinite(seconds) or seconds <= 0):
        raise SafeError("a POSIX main-thread request deadline is required")
    previous_timer = signal.getitimer(signal.ITIMER_REAL)
    if previous_timer != (0.0, 0.0):
        raise SafeError("cannot replace an active request timer")
    previous_handler = signal.getsignal(signal.SIGALRM)

    def expired(_signum, _frame):
        raise SafeError("request deadline exceeded")

    signal.signal(signal.SIGALRM, expired)
    try:
        signal.setitimer(signal.ITIMER_REAL, seconds)
        yield
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGALRM, previous_handler)
        signal.setitimer(signal.ITIMER_REAL, *previous_timer)


def call(origin, token, method, path, body=None, headers=None, timeout=REQUEST_DEADLINE):
    headers = {**(headers or {}), "Accept": "application/json", "Authorization": "Bearer " + token}
    if body is not None:
        headers["Content-Type"] = "application/json"
    req = request.Request(origin + path, data=body, headers=headers, method=method)
    opener = request.build_opener(NoRedirect())
    try:
        with deadline(timeout), opener.open(req, timeout=timeout) as response:
            if response.status < 200 or response.status >= 300:
                raise SafeError("unexpected HTTP status")
            if response.headers.get_content_type() != "application/json":
                raise SafeError("JSON response required")
            raw = response.read(MAX_RESPONSE + 1)
            if len(raw) > MAX_RESPONSE:
                raise SafeError("response exceeds size bound")
            return response.status, read_json(raw)
    except error.HTTPError as exc:
        raise HTTPStatusError(exc.code) from None
    except (OSError, error.URLError, HTTPException, ValueError):
        raise SafeError("request failed") from None


def bind_identity(identity, project, environment):
    if not isinstance(identity, dict) or identity.get("project_id") != project or identity.get("environment") != environment:
        raise SafeError("machine identity differs from explicit scope")
    if not isinstance(identity.get("permissions"), list) or not all(isinstance(p, str) for p in identity["permissions"]):
        raise SafeError("invalid machine identity response")
    for field in ("principal_id", "credential_id"):
        if not isinstance(identity.get(field), str) or not re.fullmatch(UUID, identity[field]):
            raise SafeError("invalid machine identity response")
    try:
        expires = datetime.fromisoformat(identity["expires_at"].replace("Z", "+00:00"))
        if expires.tzinfo is None or expires <= datetime.now(timezone.utc):
            raise ValueError()
    except (KeyError, ValueError, TypeError, AttributeError):
        raise SafeError("invalid or expired machine identity") from None


def private_write(path, value):
    try:
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(value, handle, ensure_ascii=False, allow_nan=False, indent=2)
            handle.write("\n")
    except (OSError, ValueError):
        raise SafeError("cannot create private output; choose a new local file") from None


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--endpoint", required=True)
    parser.add_argument("--project", required=True)
    parser.add_argument("--environment", required=True, choices=sorted(ENVIRONMENTS))
    parser.add_argument("--allow-loopback-http", action="store_true")
    auth = parser.add_mutually_exclusive_group()
    auth.add_argument("--credential-env", default="EVIA_MACHINE_TOKEN")
    auth.add_argument("--credential-file")
    parser.add_argument("--method", default="GET", choices=["GET", "POST", "PATCH", "DELETE"])
    parser.add_argument("--path", help="documented path in the selected project; omitted for identity only")
    parser.add_argument("--body-file")
    parser.add_argument("--if-match")
    parser.add_argument("--idempotency-key")
    parser.add_argument("--output", help="new mode-0600 file; may contain sensitive response data")
    args = parser.parse_args(argv)
    try:
        origin = endpoint(args.endpoint, args.allow_loopback_http)
        path = scoped_path(args.path or f"/api/v1/projects/{args.project}", args.project, args.environment)
        token = credential(args.credential_env, args.credential_file)
        if args.output and os.path.lexists(args.output):
            raise SafeError("output file already exists")
        if not args.path and (args.method != "GET" or args.body_file or args.if_match or args.idempotency_key):
            raise SafeError("identity discovery is a GET without mutation options")
        body = None
        if args.body_file:
            with open(args.body_file, "rb") as handle:
                body = handle.read(MAX_BODY + 1)
            if len(body) > MAX_BODY:
                raise SafeError("request body exceeds size bound")
            data = read_json(body)
            if not isinstance(data, dict) or ("environment" in data and data["environment"] != args.environment):
                raise SafeError("request body differs from explicit environment scope")
            if args.method == "GET":
                raise SafeError("GET cannot include a request body")
        headers = {}
        if args.if_match:
            if not re.fullmatch(r'"[1-9][0-9]{0,15}"', args.if_match):
                raise SafeError("If-Match must be a quoted positive revision")
            headers["If-Match"] = args.if_match
        if args.idempotency_key:
            if not re.fullmatch(r"[!-~]{1,128}", args.idempotency_key):
                raise SafeError("invalid idempotency key")
            headers["Idempotency-Key"] = args.idempotency_key
        status, identity = call(origin, token, "GET", "/api/v1/machine-identity")
        bind_identity(identity, args.project, args.environment)
        result = identity
        if args.path:
            status, result = call(origin, token, args.method, path, body, headers)
        if args.output:
            private_write(args.output, result)
        print(f"HTTP {status}; scoped request completed" + ("; private response saved" if args.output else "; response omitted"))
        return 0
    except HTTPStatusError as exc:
        status = f" (HTTP {exc.status})" if exc.status is not None else ""
        print(f"Request failed{status}; no response body printed.", file=sys.stderr)
        return 1
    except (SafeError, OSError, RecursionError):
        # Do not print exception strings from IO, HTTP, JSON, or server bodies.
        print("Request failed; check scope, credentials, inputs and service availability. No response body printed.", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
