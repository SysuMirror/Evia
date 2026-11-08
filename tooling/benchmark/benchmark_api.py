"""Public management fixture setup and bounded asynchronous query execution."""
from collections import Counter
import threading
import time
import urllib.parse

from benchmark_common import (HTTP, SmokeFailure, require, identifier, resource_id, timestamp,
                              loopback, safe_code, EVENT_KEY, CONTRACT_VERSION)

class ObservedHTTP(HTTP):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.statuses = Counter()
        self.lock = threading.Lock()

    def request(self, method, path, body=None, headers=None, expected=(200,)):
        try:
            value, response_headers, status = super().request(method, path, body, headers, tuple(range(200, 600)))
        except SmokeFailure:
            with self.lock:
                self.statuses["transport_or_contract_error"] += 1
            raise SmokeFailure("HTTP_TRANSPORT_OR_CONTRACT_ERROR") from None
        with self.lock:
            self.statuses[str(status)] += 1
        if status not in expected:
            code = value.get("code", "UNKNOWN_ERROR") if isinstance(value, dict) else "UNKNOWN_ERROR"
            import re
            if not isinstance(code, str) or not re.fullmatch(r"[A-Z][A-Z0-9_]{0,100}", code):
                code = "UNKNOWN_ERROR"
            raise SmokeFailure(code)
        return value, response_headers, status


class Benchmark:
    def __init__(self, api_url, settings, timeout=90):
        self.http = ObservedHTTP(loopback(api_url), settings.get("EVIA_PUBLIC_ORIGIN", "http://127.0.0.1:5173"), session=True)
        self.timeout = timeout
        self.project = None
        self.query_jobs = 0
        self.lock = threading.Lock()
        session = self.call("POST", "/api/v1/session/login", {
            "username": settings["EVIA_E2E_USERNAME"], "password": settings["EVIA_E2E_PASSWORD"]})
        self.http.csrf = session["csrf_token"]

    @property
    def base(self):
        return "/api/v1/projects/" + self.project

    def call(self, method, path, body=None, headers=None, expected=(200,)):
        return self.http.request(method, path, body, headers, expected)[0]

    def logout(self):
        if self.http.csrf:
            self.call("DELETE", "/api/v1/session", expected=(204,))
            self.http.csrf = None

    def prepare(self, uid_cardinality):
        run_id = identifier()
        project = self.call("POST", "/api/v1/projects", {"name": "Benchmark " + run_id[:12], "timezone": "UTC"}, expected=(201,))
        self.project = resource_id(project.get("id"))
        events = self.base + "/event-definitions"
        self.call("POST", events, {"event_key": EVENT_KEY, "display_name": "Synthetic benchmark page"}, expected=(201,))
        draft = self.call("GET", events + "/" + EVENT_KEY + "/draft")
        properties = {key: {"type": "number" if key in ("duration_ms", "value") else "string"}
                      for key in ("page", "section", "post_id", "duration_ms", "value", "tag")}
        example = {"page": "/page/0", "section": "section-0", "post_id": "post-0", "duration_ms": 0, "value": 0, "tag": "tag-0"}
        definition = {"display_name": "Synthetic benchmark page", "schema": {
            "type": "object", "additionalProperties": False, "properties": properties, "required": list(properties)},
            "trigger": "Explicit synthetic benchmark HTTP request", "allowed_source_kinds": ["server"],
            "fact_kind": "client_observed", "example_properties": example}
        draft = self.call("PATCH", events + "/" + EVENT_KEY + "/draft", definition,
                          {"If-Match": '"' + str(draft["revision"]) + '"'})
        valid = self.call("POST", events + "/" + EVENT_KEY + "/validate", {
            "draft_revision": draft["revision"], "sample_properties": example})
        require(valid.get("valid") is True, "Synthetic fixture schema validation failed")
        release = self.call("POST", self.base + "/releases", {"environment": "development", "expected_active_revision": 0,
            "events": [{"event_key": EVENT_KEY, "draft_revision": draft["revision"]}]}, {"Idempotency-Key": identifier()}, (202,))
        release_id = resource_id(release.get("release_id"))
        source = self.call("POST", self.base + "/sources", {"name": "Synthetic benchmark server", "environment": "development",
                           "kind": "server", "allowed_origins": []}, expected=(201,))
        deadline = time.monotonic() + self.timeout
        while True:
            release = self.call("GET", self.base + "/releases/" + release_id)
            require(release.get("state") != "failed", "Real release delivery failed")
            if release.get("state") == "published" and release.get("ingestion_ready") is True:
                break
            require(time.monotonic() < deadline, "Real release delivery timed out")
            time.sleep(.25)
        active = self.call("POST", self.base + "/release-activations", {"environment": "development", "release_id": release_id,
            "expected_active_revision": 0}, {"Idempotency-Key": identifier()})
        require(active.get("active_release_revision") == release["revision"], "Activation selected a different release")
        public = HTTP(self.http.base)
        path = "/v1/config?" + urllib.parse.urlencode({"write_key": source["write_key"], "current_release_revision": 0})
        selected, _, _ = public.request("GET", path, headers={"Authorization": "Bearer " + source["server_secret_once"]})
        require(selected.get("project_id") == self.project and selected.get("release_revision") == release["revision"],
                "Public configuration selected a different fixture")
        event = [event for event in selected.get("events", []) if event.get("event_key") == EVENT_KEY]
        require(len(event) == 1, "Public configuration omitted benchmark event")
        return {"contract_version": CONTRACT_VERSION, "api_url": self.http.base, "project_id": self.project,
                "environment": "development", "run_id": run_id, "event_key": EVENT_KEY,
                "schema_version": event[0]["schema_version"], "release_revision": release["revision"],
                "source_kind": "server", "write_key": source["write_key"], "server_secret": source["server_secret_once"],
                "source_id": resource_id(source.get("id")), "started_at": timestamp(), "uid_cardinality": uid_cardinality}

    def query(self, request, cancel=False):
        started = time.monotonic()
        path = self.base + ("/journeys/query" if request["kind"] == "journey" else "/queries")
        job = self.call("POST", path, request, expected=(202,))
        submitted = time.monotonic()
        with self.lock:
            self.query_jobs += 1
        query_id = resource_id(job.get("query_id"))
        job_path = self.base + "/queries/" + query_id
        if cancel:
            state = self.call("DELETE", job_path, expected=(202,))
            require(state.get("state") in ("canceled", "succeeded"), "Cancel returned unexpected terminal state")
            return {"submit_ms": (submitted-started)*1000, "latency_ms": (time.monotonic()-started)*1000,
                    "state": state["state"]}
        deadline = started + self.timeout
        while job.get("state") != "succeeded":
            state = job.get("state")
            require(state in ("queued", "running"), safe_code(job))
            if time.monotonic() >= deadline:
                self.call("DELETE", job_path, expected=(202,))
                raise SmokeFailure("QUERY_POLL_TIMEOUT")
            time.sleep(.05)
            job = self.call("GET", job_path)
        result = self.call("GET", job_path + "/result")
        require(result.get("query_id") == query_id and isinstance(result.get("rows"), list), "Query result contract changed")
        require(len(result["rows"]) <= 500, "Query result exceeded row bound")
        return {"submit_ms": (submitted-started)*1000, "latency_ms": (time.monotonic()-started)*1000, "result": result}


