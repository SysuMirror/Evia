#!/usr/bin/env python3
"""Exercise the real local backend through HTTP; emit only bounded safe evidence."""
import argparse
from datetime import datetime, timedelta, timezone
import http.cookiejar
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

EVENT_KEY = "smoke_page_view"
SOURCE_ORIGIN = "https://synthetic.evia.test"
MAX_QUERY_JOBS = 32
MAX_RESPONSE_BYTES = 512 * 1024


class SmokeFailure(Exception):
    pass


def require(condition, message):
    if not condition:
        raise SmokeFailure(message)


def identifier():
    return str(uuid.uuid4())


def resource_id(value):
    require(isinstance(value, str) and len(value) == 36, "API returned an invalid resource ID")
    try:
        uuid.UUID(value)
    except ValueError:
        raise SmokeFailure("API returned an invalid resource ID") from None
    return value


def timestamp(value=None):
    return (value or datetime.now(timezone.utc)).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def definition():
    return {
        "display_name": "Synthetic page event",
        "schema": {
            "type": "object",
            "description": "Internal synthetic verification annotation",
            "additionalProperties": False,
            "properties": {"page": {"type": "string", "minLength": 1, "maxLength": 64}},
            "required": ["page"],
        },
        "trigger": "Explicit synthetic HTTP verification request",
        "allowed_source_kinds": ["browser", "server"],
        "fact_kind": "client_observed",
        "example_properties": {"page": "synthetic"},
    }


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, target):
        return None


class HTTP:
    def __init__(self, base, origin=None, session=False):
        self.base, self.origin, self.csrf = base.rstrip("/"), origin, None
        handlers = [NoRedirect()]
        if session:
            handlers.append(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        self.opener = urllib.request.build_opener(*handlers)

    def request(self, method, path, body=None, headers=None, expected=(200,)):
        request_headers = {"Accept": "application/json", **(headers or {})}
        if self.origin:
            request_headers.setdefault("Origin", self.origin)
        if self.csrf and method not in ("GET", "HEAD", "OPTIONS"):
            request_headers["X-CSRF-Token"] = self.csrf
        payload = None
        if body is not None:
            payload = json.dumps(body, separators=(",", ":"), allow_nan=False).encode()
            request_headers["Content-Type"] = "application/json"
        request = urllib.request.Request(self.base + path, data=payload, headers=request_headers, method=method)
        try:
            response = self.opener.open(request, timeout=10)
        except urllib.error.HTTPError as error:
            response = error
        except (OSError, urllib.error.URLError) as error:
            raise SmokeFailure(f"HTTP connection failed ({type(error).__name__})") from None
        with response:
            raw = response.read(MAX_RESPONSE_BYTES + 1)
            require(len(raw) <= MAX_RESPONSE_BYTES, "HTTP response exceeded the smoke byte bound")
            status, response_headers = response.code, response.headers
        try:
            value = json.loads(raw) if raw else None
        except (ValueError, UnicodeError):
            raise SmokeFailure(f"HTTP {status} returned non-JSON content") from None
        if status not in expected:
            code = value.get("code", "UNKNOWN_ERROR") if isinstance(value, dict) else "UNKNOWN_ERROR"
            code = code if isinstance(code, str) and re.fullmatch(r"[A-Z][A-Z0-9_]{0,100}", code) else "UNKNOWN_ERROR"
            request_id = response_headers.get("X-Request-ID", "")
            request_id = request_id if re.fullmatch(r"[a-zA-Z0-9-]{1,100}", request_id) else "unavailable"
            # The query string can contain a public write credential; never log it.
            route = path.partition("?")[0]
            raise SmokeFailure(f"{method} {route}: HTTP {status}, {code}, request_id={request_id}")
        if path.startswith(("/api/", "/v1/")):
            require(bool(response_headers.get("X-Request-ID")), "API response omitted X-Request-ID")
        return value, response_headers, status


class Smoke:
    def __init__(self, api_url, console_origin, timeout):
        self.control = HTTP(api_url, console_origin, session=True)
        # Collection/config use a different opener with no cookie jar.
        self.public = HTTP(api_url)
        self.timeout, self.stage, self.query_jobs = timeout, "configuration", 0
        self.project, self.release, self.sources = None, None, {}
        self.started = datetime.now(timezone.utc) - timedelta(seconds=5)
        self.uids = ["synthetic-" + uuid.uuid4().hex for _ in range(2)]
        self.event_id = identifier()
        self.session_id, self.tab_id = identifier(), identifier()
        self.page_id, self.next_page_id = identifier(), identifier()

    def evidence(self, stage, **values):
        print(json.dumps({"stage": stage, **values}, sort_keys=True), flush=True)

    @property
    def base(self):
        return "/api/v1/projects/" + self.project

    def management(self, method, path, body=None, headers=None, expected=(200,)):
        return self.control.request(method, path, body, headers, expected)[0]

    def wait(self, read, ready, label, timeout=None):
        deadline = time.monotonic() + (timeout or self.timeout)
        while True:
            value = read()
            if ready(value):
                return value
            require(time.monotonic() < deadline, label + " did not become ready before the deadline")
            time.sleep(0.25)

    def event(self, version, revision, user=0, server=False):
        context = {
            "session_id": self.session_id if user == 0 else identifier(),
            "tab_id": self.tab_id,
            "sequence": 3 if server else user + 1,
            "page_view_id": self.next_page_id if server else self.page_id,
            "route_name": "Synthetic",
            "route_template": "/synthetic",
        }
        if server:
            context.update({"source_page_view_id": self.page_id, "navigation_id": identifier(), "operation_id": "synthetic-operation"})
        return {
            "event_id": self.event_id if user == 0 else identifier(),
            "event_key": EVENT_KEY,
            "schema_version": version,
            "release_revision": revision,
            "occurred_at": timestamp(self.started + timedelta(seconds=2 if server else user)),
            "identity": {"uid": self.uids[user]},
            "context": context,
            "sdk": {"name": "synthetic-http-smoke", "version": "1.0.0"},
            "properties": {"page": "synthetic"},
        }

    def batch(self, source, events):
        return {"protocol_version": 1, "batch_id": identifier(), "write_key": source["write_key"], "sent_at": timestamp(), "events": events}

    def source_headers(self, kind):
        if kind == "browser":
            return {"Origin": SOURCE_ORIGIN}
        return {"Authorization": "Bearer " + self.sources[kind]["server_secret_once"]}

    def collect(self, kind, batch, partial=False):
        receipt, _, _ = self.public.request("POST", "/v1/collect", batch, self.source_headers(kind), (202,))
        require(receipt.get("batch_id") == batch["batch_id"], "Collection acknowledged a different batch")
        results = receipt.get("results", [])
        require(len(results) == len(batch["events"]), "Collection omitted item results")
        for index, result in enumerate(results):
            require(result.get("index") == index, "Collection item positions changed")
            if partial and index == 1:
                require(result.get("status") == "rejected", "Invalid sibling was accepted")
                require(result.get("error", {}).get("code") == "PROPERTY_SCHEMA_MISMATCH", "Invalid sibling rejection did not identify its schema mismatch")
                require(result["error"].get("retryable") is False, "Invalid sibling was incorrectly retryable")
            else:
                require(result.get("status") == "accepted", "Valid event was not accepted by the real queue")
                require(result.get("event_id") == batch["events"][index]["event_id"], "Collection changed an event ID")
        return receipt

    def query(self, kind, **fields):
        self.query_jobs += 1
        require(self.query_jobs <= MAX_QUERY_JOBS, "Smoke query-job budget of 32 was exhausted")
        body = {
            "kind": kind, "environment": "development", "timezone": "UTC",
            "time_range": {"from": timestamp(self.started - timedelta(minutes=1)), "to": timestamp(self.started + timedelta(minutes=5))},
            "event_keys": [EVENT_KEY], "limit": 20, **fields,
        }
        path = self.base + ("/journeys/query" if kind == "journey" else "/queries")
        job = self.management("POST", path, body, expected=(202,))
        query_id = resource_id(job.get("query_id"))

        def ready(value):
            state = value.get("state")
            if state in ("failed", "canceled", "expired"):
                code = value.get("error", {}).get("code", "NO_ERROR_CODE")
                safe = code if isinstance(code, str) and re.fullmatch(r"[A-Z][A-Z0-9_]{0,100}", code) else "UNKNOWN_ERROR"
                raise SmokeFailure(f"Query {query_id} ended as {state}: {safe}")
            require(state in ("queued", "running", "succeeded"), "Query returned an unknown state")
            return state == "succeeded"

        self.wait(lambda: self.management("GET", self.base + "/queries/" + query_id), ready, "Query " + query_id)
        result = self.management("GET", self.base + "/queries/" + query_id + "/result")
        require(result.get("query_id") == query_id, "Query result scope changed")
        require(result.get("metadata", {}).get("coverage", {}).get("state") == "provisional", "Smoke expected honest provisional coverage")
        return result

    def metrics(self):
        result = self.query("trend", subject="uid", metrics=[{"op": "count", "accuracy": "exact"}, {"op": "unique", "accuracy": "exact"}])
        require(result.get("metadata", {}).get("accuracy") == "exact", "Metric accuracy is not exact")
        rows = result.get("rows", [])
        require(len(rows) == 1, "Ungrouped count query returned an unexpected row count")
        counts = rows[0].get("metric_0"), rows[0].get("metric_1")
        require(all(isinstance(value, (int, float)) and not isinstance(value, bool) and value >= 0 for value in counts), "Metric values are invalid")
        require(counts[0] <= 3 and counts[1] <= 2, "Duplicate, rejected or unauthorized events reached the result")
        require(counts[1] <= counts[0], "Unique count exceeded the selected event count")
        if counts[0] == 3:
            require(counts[1] == 2, "Fully visible events have the wrong exact UID count")
        return counts

    def publish(self):
        self.stage = "project_and_draft"
        project = self.management("POST", "/api/v1/projects", {"name": "HTTP smoke " + uuid.uuid4().hex[:12], "timezone": "UTC"}, expected=(201,))
        self.project = resource_id(project.get("id"))
        self.evidence(self.stage, project_id=self.project)
        events = self.base + "/event-definitions"
        self.management("POST", events, {"event_key": EVENT_KEY, "display_name": "Synthetic page event"}, expected=(201,))
        draft = self.management("GET", events + "/" + EVENT_KEY + "/draft")
        draft = self.management("PATCH", events + "/" + EVENT_KEY + "/draft", definition(), {"If-Match": '"' + str(draft["revision"]) + '"'})
        validation = self.management("POST", events + "/" + EVENT_KEY + "/validate", {"draft_revision": draft["revision"], "sample_properties": {"page": "synthetic"}})
        require(validation.get("valid") is True, "Published fixture draft did not validate")
        self.stage = "publish_and_delivery"
        release = self.management("POST", self.base + "/releases", {"environment": "development", "expected_active_revision": 0, "events": [{"event_key": EVENT_KEY, "draft_revision": draft["revision"]}]}, {"Idempotency-Key": identifier()}, (202,))
        self.release = resource_id(release.get("release_id"))
        require(release.get("state") == "published", "Release definition was not published")
        for kind in ("browser", "server"):
            body = {"name": "Synthetic " + kind, "environment": "development", "kind": kind, "allowed_origins": [SOURCE_ORIGIN] if kind == "browser" else []}
            self.sources[kind] = self.management("POST", self.base + "/sources", body, expected=(201,))
            resource_id(self.sources[kind].get("id"))
            require(isinstance(self.sources[kind].get("write_key"), str) and 1 <= len(self.sources[kind]["write_key"]) <= 128, "Source creation omitted its bounded write credential")
            if kind == "server":
                require(isinstance(self.sources[kind].get("server_secret_once"), str) and len(self.sources[kind]["server_secret_once"]) == 43, "Server creation omitted its bounded one-time secret")

        def release_ready(value):
            require(value.get("state") != "failed", "Release delivery failed")
            return value.get("state") == "published" and value.get("ingestion_ready") is True

        release = self.wait(lambda: self.management("GET", self.base + "/releases/" + self.release), release_ready, "Real release delivery")
        self.stage = "activation_and_public_config"
        activation = self.management("POST", self.base + "/release-activations", {"environment": "development", "release_id": self.release, "expected_active_revision": 0}, {"Idempotency-Key": identifier()})
        require(activation.get("active_release_revision") == release["revision"], "Activation selected a different release")
        versions = []
        for kind in ("browser", "server"):
            path = "/v1/config?" + urllib.parse.urlencode({"write_key": self.sources[kind]["write_key"], "current_release_revision": 0})
            config, headers, _ = self.public.request("GET", path, headers=self.source_headers(kind))
            require(config.get("project_id") == self.project and config.get("environment") == "development", "Public config scope changed")
            require(config.get("release_revision") == release["revision"], "Public config did not expose the activated release")
            selected = [event for event in config.get("events", []) if event.get("event_key") == EVENT_KEY]
            require(len(selected) == 1, "Public config omitted the selected event")
            require("description" not in selected[0]["schema"], "Public config leaked internal schema annotations")
            require(not any(key in config for key in ("trigger", "example_properties", "server_secret_once")), "Public config exposed internal metadata")
            versions.append(selected[0]["schema_version"])
            require(bool(headers.get("ETag")), "Public config omitted its content ETag")
            self.public.request("GET", path, headers={**self.source_headers(kind), "If-None-Match": headers["ETag"]}, expected=(304,))
        require(versions[0] == versions[1], "Sources received different versions of the same fixture")
        self.evidence(self.stage, release_id=self.release, release_revision=release["revision"], source_ids=[self.sources[kind]["id"] for kind in ("browser", "server")])
        return versions[0], release["revision"]

    def run(self, username, password):
        self.stage = "login"
        session = self.management("POST", "/api/v1/session/login", {"username": username, "password": password})
        self.control.csrf = session["csrf_token"]
        version, revision = self.publish()
        self.stage = "collection_and_partial_batch"
        first = self.event(version, revision)
        invalid = self.event(version, revision, user=1)
        invalid["properties"]["unexpected_field"] = True
        browser = self.batch(self.sources["browser"], [first, invalid])
        self.collect("browser", browser, partial=True)
        self.collect("browser", browser, partial=True)  # Same original event and batch IDs.
        self.collect("browser", self.batch(self.sources["browser"], [self.event(version, revision, user=1)]))
        self.collect("server", self.batch(self.sources["server"], [self.event(version, revision, server=True)]))
        self.evidence(self.stage, accepted_submissions=4, permanently_rejected_items=2, expected_source_scoped_events=3)
        self.stage = "wrong_origin_and_bearer"
        denied_event = self.event(version, revision)
        denied_event["event_id"] = identifier()
        denied_browser = self.batch(self.sources["browser"], [denied_event])
        value, _, _ = self.public.request("POST", "/v1/collect", denied_browser, {"Origin": "https://denied.evia.test"}, (403,))
        require(value.get("code") == "SOURCE_ORIGIN_FORBIDDEN", "Wrong Origin was not rejected by source policy")
        denied_event = self.event(version, revision, server=True)
        denied_event["event_id"] = identifier()
        server = self.batch(self.sources["server"], [denied_event])
        value, _, _ = self.public.request("POST", "/v1/collect", server, {"Authorization": "Bearer " + "x" * 43}, (401,))
        require(value.get("code") == "SOURCE_CREDENTIAL_INVALID", "Wrong server bearer was not rejected")
        self.stage = "query_visibility_and_exact_metrics"
        self.wait(self.metrics, lambda value: value == (3, 2), "Queue-to-query visibility")
        self.evidence(self.stage, selected_page_event_count=3, exact_uid_unique=2, query_jobs=self.query_jobs)
        self.stage = "source_scoped_journey"
        journey = self.query("journey", subject="uid", identity_filter={"uid": self.uids[0]})
        rows = journey.get("rows", [])
        expected = {(self.sources[kind]["id"], self.event_id) for kind in ("browser", "server")}
        require(len(rows) == 2 and {(row.get("source_id"), row.get("event_id")) for row in rows} == expected, "Journey lost source-scoped identity or retained an exact retry")
        require(all(row.get("uid") == self.uids[0] for row in rows), "Journey crossed a raw UID boundary")
        require([row.get("source_kind") for row in rows] == ["browser", "server"], "Journey ordering or source kinds changed")
        require(rows[1].get("context", {}).get("source_page_view_id") == self.page_id, "Journey dropped a context link")
        self.evidence(self.stage, journey_rows=len(rows), distinct_source_pairs=len(expected))
        self.stage = "disable_after_cache_expiry"
        disabled_at = time.monotonic()
        for kind in ("browser", "server"):
            path = self.base + "/sources/" + self.sources[kind]["id"]
            current = self.management("GET", path)
            self.management("PATCH", path, {"status": "disabled"}, {"If-Match": '"' + str(current["revision"]) + '"'})
            self.management("GET", path)  # Start each post-commit observation window promptly.
        for kind in ("browser", "server"):
            path = self.base + "/sources/" + self.sources[kind]["id"]
            self.wait(lambda: self.management("GET", path), lambda value: value.get("status") == "disabled" and value.get("application_state") == "applied" and time.monotonic() - disabled_at >= 2.05, "Source cache expiry", timeout=8)
            attempt = self.event(version, revision)
            attempt["event_id"] = identifier()
            value, _, _ = self.public.request("POST", "/v1/collect", self.batch(self.sources[kind], [attempt]), self.source_headers(kind), (403,))
            require(value.get("code") == "SOURCE_DISABLED", "Disabled source did not reject collection after cache expiry")
        require(self.metrics() == (3, 2), "Metrics changed after rejection and cache expiry")
        self.evidence(self.stage, disabled_sources=2, selected_page_event_count=3, exact_uid_unique=2)

    def logout(self):
        if self.control.csrf:
            self.control.request("DELETE", "/api/v1/session", expected=(204,))
            self.control.csrf = None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--api-url", default=os.environ.get("EVIA_E2E_API_URL", "http://127.0.0.1:8080"))
    parser.add_argument("--console-origin", default=os.environ.get("EVIA_PUBLIC_ORIGIN", "http://127.0.0.1:5173"))
    parser.add_argument("--timeout", type=int, default=60, help="Bound each readiness/query phase to this many seconds (5–180)")
    arguments = parser.parse_args()
    endpoint = urllib.parse.urlsplit(arguments.api_url)
    require(endpoint.scheme in ("http", "https") and endpoint.hostname in ("127.0.0.1", "localhost", "::1") and endpoint.path in ("", "/") and not endpoint.query and not endpoint.fragment and endpoint.username is None, "Smoke API URL must be a loopback HTTP(S) origin")
    require(5 <= arguments.timeout <= 180, "Smoke timeout must be between 5 and 180 seconds")
    username, password = os.environ.get("EVIA_E2E_USERNAME"), os.environ.get("EVIA_E2E_PASSWORD")
    require(bool(username and password), "EVIA_E2E_USERNAME and EVIA_E2E_PASSWORD are required")
    smoke = Smoke(arguments.api_url, arguments.console_origin, arguments.timeout)
    failed = False
    try:
        smoke.run(username, password)
        smoke.stage = "logout"
        smoke.logout()
        smoke.evidence("passed", project_id=smoke.project, release_id=smoke.release, query_jobs=smoke.query_jobs)
    except Exception as error:
        failed = True
        message = str(error) if isinstance(error, SmokeFailure) else "Unexpected local failure: " + type(error).__name__
        smoke.evidence("failed", failed_stage=smoke.stage, project_id=smoke.project, reason=message)
    finally:
        if failed:
            try:
                smoke.logout()
            except Exception:
                pass
    return 1 if failed else 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except SmokeFailure as error:
        print(json.dumps({"stage": "configuration_failed", "reason": str(error)}), file=sys.stderr)
        raise SystemExit(1)
