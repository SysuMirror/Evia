"""Validate authoritative contracts and shared fixtures with offline references."""
import copy
import json
from pathlib import Path

from jsonschema import Draft202012Validator, FormatChecker
from openapi_spec_validator import validate

ROOT = Path(__file__).resolve().parents[1]
CONTRACTS = ROOT / "contracts"


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON property: {key}")
        result[key] = value
    return result


def invalid_constant(value):
    raise ValueError(f"invalid JSON constant: {value}")


def read(path):
    return json.loads(
        path.read_text(encoding="utf-8"),
        object_pairs_hook=unique_object,
        parse_constant=invalid_constant,
    )


for path in sorted(CONTRACTS.rglob("*.json")):
    document = read(path)
    if path.name.endswith(".schema.json"):
        Draft202012Validator.check_schema(document)

spec = CONTRACTS / "http/openapi.json"
validate(read(spec), base_uri=spec.as_uri())

# The same generated bundle is embedded in the Go API. Generation drift is
# checked separately; references must resolve inside this local bundle.
bundle = read(ROOT / "backend/internal/gen/wire/contracts.gen.json")
Draft202012Validator.check_schema(bundle)
formats = FormatChecker()


def validator(name):
    return Draft202012Validator(
        {"$ref": f"#/$defs/{name}", "$defs": bundle["$defs"]},
        format_checker=formats,
    )


fixtures = {
    "collect-browser-request.json": "CollectRequest",
    "collect-server-request.json": "CollectRequest",
    "collect-partial-response.json": "CollectResponse",
    "learning-site-event-plan.json": "EventPlan",
    "journey-query.json": "QueryRequest",
    "journey-result.json": "QueryResult",
}
for filename, schema in fixtures.items():
    validator(schema).validate(read(CONTRACTS / "fixtures" / filename))

journey = read(CONTRACTS / "fixtures/uid-journey-fixture.json")
for item in journey["items"]:
    validator("EventEnvelope").validate(item["event"])

system = read(CONTRACTS / "fixtures/system-events.json")
for event in system["events"]:
    Draft202012Validator.check_schema(event["schema"])
    Draft202012Validator(event["schema"], format_checker=formats).validate(
        event["example_properties"]
    )

# Corrupt shared fixtures to exercise strict fields and formats through the
# generated definitions rather than handwritten shapes.
request = read(CONTRACTS / "fixtures/collect-browser-request.json")
bad_unknown = copy.deepcopy(request)
bad_unknown["unexpected_field"] = True
bad_timestamp = copy.deepcopy(request)
bad_timestamp["sent_at"] = "yesterday"
bad_missing = copy.deepcopy(request)
del bad_missing["events"]
for label, value in (
    ("unknown field", bad_unknown),
    ("timestamp format", bad_timestamp),
    ("missing field", bad_missing),
):
    if validator("CollectRequest").is_valid(value):
        raise AssertionError(f"generated contract accepted invalid {label}")

# Saved definitions reuse query field/filter contracts, but exclude execution
# context and server-resolved types. Exercise the actual generated bundle for
# requests and complete resource responses, including false, zero and arrays.
saved_definition = {
    "title": "Saved activity",
    "description": "",
    "query": {
        "kind": "trend",
        "event_versions": [{"event_key": "activity_view", "schema_version": 1}],
        "metrics": [{"op": "count", "accuracy": "exact"}],
        "filter": {
            "op": "and",
            "args": [
                {"op": "eq", "field": {"kind": "property", "event_key": "activity_view", "schema_version": 1, "key": "enabled"}, "value": False},
                {"op": "in", "field": {"kind": "property", "event_key": "activity_view", "schema_version": 1, "key": "score"}, "value": [0, 1]},
            ],
        },
    },
    "visualization": {"kind": "metric"},
}
saved_id = "00000000-0000-4000-8000-000000000001"
saved_resource = {
    "analysis_id": saved_id, "project_id": saved_id,
    "environment": "development", "title": "Saved activity", "kind": "trend",
    "draft_revision": 1, "published_version": 0,
    "created_at": "2026-11-08T00:00:00Z", "updated_at": "2026-11-08T00:00:00Z",
}
saved_dependencies = {
    "project_id": saved_id, "environment": "development",
    "selection_mode": "event_versions", "unfixed_event_keys": [],
    "event_versions": [{"event_key": "activity_view", "schema_version": 1, "definition_hash": "a" * 64}],
}
saved_validation = {
    "valid": True, "execution_performed": False, "draft_revision": 1,
    "published_version": 0, "normalized_definition": saved_definition,
    "dependencies": saved_dependencies, "definition_hash": "b" * 64,
    "dependency_hash": "c" * 64, "validation_hash": "d" * 64,
    "diff": [{"category": "definition", "path": "/definition", "after": saved_definition}],
}
saved_version = {
    "analysis_id": saved_id, "project_id": saved_id, "environment": "development",
    "version": 1, "draft_revision": 1, "definition": saved_definition,
    "dependencies": saved_dependencies, "definition_hash": "b" * 64,
    "dependency_hash": "c" * 64, "validation_hash": "d" * 64,
    "created_at": "2026-11-08T00:00:00Z", "created_by": saved_id,
}
saved_create = {"environment": "development", "definition": saved_definition}
saved_execute = {"time_range": {"from": "2026-11-08T00:00:00Z", "to": "2026-11-08T01:00:00Z"}, "timezone": "UTC"}
saved_cases = {
    "SavedAnalysisCreate": saved_create,
    "SavedAnalysisUpdate": {"definition": saved_definition},
    "SavedAnalysisValidate": {"draft_revision": 1, "expected_published_version": 0},
    "SavedAnalysisPublish": {"draft_revision": 1, "expected_published_version": 0, "validation_hash": "d" * 64},
    "SavedAnalysisExecute": saved_execute,
    "SavedAnalysis": saved_resource,
    "SavedAnalysisDraft": {**saved_resource, "definition": saved_definition},
    "SavedAnalysisValidation": saved_validation,
    "SavedAnalysisVersion": saved_version,
    "SavedAnalysisList": {"items": [saved_resource], "next_cursor": None},
    "SavedAnalysisVersionList": {"items": [saved_version], "next_cursor": None},
}
for name, value in saved_cases.items():
    validator(name).validate(value)
    unknown = {**value, "unexpected_field": True}
    if validator(name).is_valid(unknown):
        raise AssertionError(f"generated {name} accepted unknown field")

saved_negative_count = len(saved_cases)


def reject_saved(name, label, original, path, value=None, remove=False):
    global saved_negative_count
    bad = copy.deepcopy(original)
    parent = bad
    for key in path[:-1]:
        parent = parent[key]
    if remove:
        del parent[path[-1]]
    else:
        parent[path[-1]] = value
    if validator(name).is_valid(bad):
        raise AssertionError(f"generated {name} accepted invalid {label}")
    saved_negative_count += 1


for field, value in (
    ("environment", "production"), ("time_range", saved_execute["time_range"]),
    ("timezone", "UTC"), ("identity_filter", {"uid": "viewer"}),
    ("identity_revision", 1), ("event_keys", ["activity_view"]),
    ("cursor", "cursor"), ("resolved_type", "number"),
):
    reject_saved("SavedAnalysisCreate", field, saved_create, ["definition", "query", field], value)
for label, path, value in (
    ("unknown definition", ["unexpected"], True),
    ("unsafe version", ["query", "event_versions", 0, "schema_version"], 9007199254740992),
    ("missing pinned versions", ["query", "event_versions"], []),
    ("unknown layout option", ["visualization", "script"], "executable"),
    ("unsupported visualization", ["visualization", "kind"], "custom"),
    ("invalid metric visualization", ["query", "interval"], "day"),
    ("unbounded title", ["title"], "a" * 129),
    ("empty title", ["title"], "  "),
    ("unbounded description", ["description"], "a" * 2001),
    ("resolved property type", ["query", "filter", "args", 0, "field", "resolved_type"], "boolean"),
    ("unsafe filter number", ["query", "filter", "args", 1, "value"], 9007199254740992),
    ("unbounded filter array", ["query", "filter", "args", 1, "value"], [0] * 21),
    ("missing event pin", ["query", "event_versions", 0, "schema_version"], None),
    ("invalid query kind", ["query", "kind"], "events"),
):
    reject_saved("SavedAnalysisDefinition", label, saved_definition, path, value)
for field in ("draft_revision", "expected_published_version"):
    reject_saved("SavedAnalysisValidate", "unsafe " + field, saved_cases["SavedAnalysisValidate"], [field], 9007199254740992)
reject_saved("SavedAnalysisPublish", "malformed hash", saved_cases["SavedAnalysisPublish"], ["validation_hash"], "invalid")
reject_saved("SavedAnalysisExecute", "invalid timestamp", saved_execute, ["time_range", "from"], "yesterday")
reject_saved("SavedAnalysisExecute", "environment override", saved_execute, ["environment"], "production")
reject_saved("SavedAnalysisValidation", "execution performed", saved_validation, ["execution_performed"], True)
reject_saved("SavedAnalysisValidation", "unfixed manifest", saved_validation, ["dependencies", "unfixed_event_keys"], ["activity_view"])
reject_saved("SavedAnalysisValidation", "unfixed selection", saved_validation, ["dependencies", "selection_mode"], "all_events")
reject_saved("SavedAnalysisValidation", "unbounded diff", saved_validation, ["diff"], saved_validation["diff"] * 33)
reject_saved("SavedAnalysisVersion", "unsafe version", saved_version, ["version"], 9007199254740992)
reject_saved("SavedAnalysisList", "unbounded list", saved_cases["SavedAnalysisList"], ["items"], [saved_resource] * 101)
reject_saved("SavedAnalysisVersionList", "unbounded version list", saved_cases["SavedAnalysisVersionList"], ["items"], [saved_version] * 51)

funnel_definition = copy.deepcopy(saved_definition)
funnel_definition["query"] = {
    "kind": "funnel", "subject": "uid",
    "funnel": {"steps": [{"event_key": "activity_view", "schema_version": 1}, {"event_key": "activity_complete", "schema_version": 1}], "window_seconds": 3600, "mode": "ordered_with_interleaving"},
}
funnel_definition["visualization"]["kind"] = "funnel"
validator("SavedAnalysisDefinition").validate(funnel_definition)
reject_saved("SavedAnalysisDefinition", "funnel event_versions", funnel_definition, ["query", "event_versions"], saved_definition["query"]["event_versions"])
reject_saved("SavedAnalysisDefinition", "unsafe funnel step", funnel_definition, ["query", "funnel", "steps", 0, "schema_version"], 9007199254740992)
reject_saved("SavedAnalysisDefinition", "unbounded funnel", funnel_definition, ["query", "funnel", "steps"], funnel_definition["query"]["funnel"]["steps"] * 5)

print(
    f"OpenAPI and JSON Schemas verified; {len(fixtures)} shared fixtures, "
    f"{len(journey['items'])} journey events, {len(system['events'])} system "
    f"property examples, {len(saved_cases) + 1} saved-analysis cases, "
    f"three collection and {saved_negative_count} saved-analysis negative cases passed."
)
