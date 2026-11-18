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

retention_definition = copy.deepcopy(saved_definition)
retention_definition["query"] = {
    "kind": "retention", "subject": "uid",
    "retention": {
        "start_event": {"event_key": "sign_up", "schema_version": 1},
        "return_event": {"event_key": "login", "schema_version": 1},
        "granularity": "day", "periods": 4,
    },
}
retention_definition["visualization"]["kind"] = "table"
validator("SavedAnalysisDefinition").validate(retention_definition)
validator("SavedAnalysisTemplate").validate(retention_definition["query"])
reject_saved("SavedAnalysisDefinition", "retention event_versions", retention_definition, ["query", "event_versions"], saved_definition["query"]["event_versions"])
reject_saved("SavedAnalysisDefinition", "retention without subject", retention_definition, ["query", "subject"], None)
reject_saved("SavedAnalysisDefinition", "retention funnel knob", retention_definition, ["query", "funnel"], funnel_definition["query"]["funnel"])
reject_saved("SavedAnalysisDefinition", "retention bad granularity", retention_definition, ["query", "retention", "granularity"], "hour")
reject_saved("SavedAnalysisDefinition", "retention unbounded periods", retention_definition, ["query", "retention", "periods"], 13)
reject_saved("SavedAnalysisDefinition", "retention unpinned start", retention_definition, ["query", "retention", "start_event", "schema_version"], 0)

segment_definition = copy.deepcopy(saved_definition)
segment_definition["query"] = {
    "kind": "segment", "subject": "uid",
    "segment": {"event": {"event_key": "sign_up", "schema_version": 1},
                "field": {"kind": "property", "event_key": "sign_up", "schema_version": 1, "key": "plan"}},
}
segment_definition["visualization"]["kind"] = "table"
validator("SavedAnalysisDefinition").validate(segment_definition)
validator("SavedAnalysisTemplate").validate(segment_definition["query"])
reject_saved("SavedAnalysisDefinition", "segment event_versions", segment_definition, ["query", "event_versions"], saved_definition["query"]["event_versions"])
reject_saved("SavedAnalysisDefinition", "segment without subject", segment_definition, ["query", "subject"], None)
reject_saved("SavedAnalysisDefinition", "segment missing block", segment_definition, ["query", "segment"], None)
reject_saved("SavedAnalysisDefinition", "segment unpinned event", segment_definition, ["query", "segment", "event", "schema_version"], 0)

path_definition = copy.deepcopy(saved_definition)
path_definition["query"] = {
    "kind": "path", "subject": "uid",
    "path": {"depth": 3, "start_event": {"event_key": "open", "schema_version": 1}},
}
path_definition["visualization"]["kind"] = "table"
validator("SavedAnalysisDefinition").validate(path_definition)
validator("SavedAnalysisTemplate").validate(path_definition["query"])
unpinned_path = copy.deepcopy(path_definition)
del unpinned_path["query"]["path"]["start_event"]
validator("SavedAnalysisTemplate").validate(unpinned_path["query"])
reject_saved("SavedAnalysisDefinition", "path bad depth", path_definition, ["query", "path", "depth"], 1)
reject_saved("SavedAnalysisDefinition", "path unpinned start", path_definition, ["query", "path", "start_event", "schema_version"], 0)
reject_saved("SavedAnalysisDefinition", "path event_versions", path_definition, ["query", "event_versions"], saved_definition["query"]["event_versions"])

attribution_definition = copy.deepcopy(saved_definition)
attribution_definition["query"] = {
    "kind": "attribution", "subject": "uid",
    "attribution": {
        "conversion": {"event_key": "buy", "schema_version": 1},
        "touchpoints": [{"event_key": "ad_click", "schema_version": 1}],
        "window_seconds": 86400, "model": "last_touch",
    },
}
attribution_definition["visualization"]["kind"] = "table"
validator("SavedAnalysisDefinition").validate(attribution_definition)
validator("SavedAnalysisTemplate").validate(attribution_definition["query"])
reject_saved("SavedAnalysisDefinition", "attribution bad window", attribution_definition, ["query", "attribution", "window_seconds"], 0)
reject_saved("SavedAnalysisDefinition", "attribution unknown model", attribution_definition, ["query", "attribution", "model"], "data_driven")
reject_saved("SavedAnalysisDefinition", "attribution event_versions", attribution_definition, ["query", "event_versions"], saved_definition["query"]["event_versions"])

print(
    f"OpenAPI and JSON Schemas verified; {len(fixtures)} shared fixtures, "
    f"{len(journey['items'])} journey events, {len(system['events'])} system "
    f"property examples, {len(saved_cases) + 1} saved-analysis cases, "
    f"three collection and {saved_negative_count} saved-analysis negative cases passed."
)

# Independent metrics reuse pinned query primitives, never saved-analysis IDs.
metric_definition = {
    "title": "Activity count", "description": "",
    "event_versions": saved_definition["query"]["event_versions"],
    "subject": "uid", "aggregation": {"op": "count", "accuracy": "exact"},
    "unit": "event", "filter": saved_definition["query"]["filter"],
}
metric_resource = {key: value for key, value in saved_resource.items() if key not in {"kind", "analysis_id"}}
metric_resource["metric_id"] = saved_id
metric_validation = {**saved_validation, "normalized_definition": metric_definition, "diff": [{"category": "definition", "path": "/definition", "after": metric_definition}]}
metric_version = {key: value for key, value in saved_version.items() if key != "analysis_id"}
metric_version.update(metric_id=saved_id, definition=metric_definition)
metric_execute = {**saved_execute, "interval": "day", "limit": 500}
metric_cases = {
    "MetricDefinition": metric_definition,
    "MetricRecord": metric_resource,
    "MetricDraft": {**metric_resource, "definition": metric_definition},
    "MetricVersion": metric_version,
    "MetricValidation": metric_validation,
    "MetricCreate": {"environment": "development", "definition": metric_definition},
    "MetricUpdate": {"definition": metric_definition},
    "MetricValidate": saved_cases["SavedAnalysisValidate"],
    "MetricPublish": saved_cases["SavedAnalysisPublish"],
    "MetricExecute": metric_execute,
    "MetricList": {"items": [metric_resource], "next_cursor": None},
    "MetricVersionList": {"items": [metric_version], "next_cursor": None},
}
for name, value in metric_cases.items():
    validator(name).validate(value)
    if validator(name).is_valid({**value, "unexpected_field": True}):
        raise AssertionError(f"generated {name} accepted unknown field")
validator("MetricExecute").validate(saved_execute)  # Whole-window aggregation.
metric_negative_count = 0
for op, subject, field, allowed in (
    ("count", "uid", None, {"event"}),
    ("unique", "uid", None, {"raw_uid"}),
    ("unique", "anonymous", None, {"anonymous_id"}),
    ("unique", "uid", {"kind": "property", "event_key": "activity_view", "schema_version": 1, "key": "score"}, {"distinct_value"}),
    *((op, "uid", {"kind": "property", "event_key": "activity_view", "schema_version": 1, "key": "score"}, {"number", "millisecond", "second", "byte"}) for op in ("sum", "avg", "min", "max", "p95")),
):
    for unit in ("event", "raw_uid", "anonymous_id", "distinct_value", "number", "millisecond", "second", "byte", "unknown"):
        value = {**metric_definition, "subject": subject, "aggregation": {"op": op}, "unit": unit}
        if field is not None:
            value["aggregation"]["field"] = field
        if validator("MetricDefinition").is_valid(value) != (unit in allowed):
            raise AssertionError(f"generated metric unit policy mismatch {op}/{subject}/{unit}")
        metric_negative_count += int(unit not in allowed)
for field, value in (("query", {}), ("visualization", {}), ("metric_id", saved_id), ("formula", "a+b"), ("environment", "production"), ("event_versions", []), ("subject", "resolved_person")):
    reject_saved("MetricDefinition", field, metric_definition, [field], value)
reject_saved("MetricDefinition", "resolved type", metric_definition, ["filter", "args", 0, "field", "resolved_type"], "boolean")
reject_saved("MetricDefinition", "unpinned version", metric_definition, ["event_versions", 0, "schema_version"], 0)
reject_saved("MetricExecute", "group budget", metric_execute, ["group_by"], [{"kind": "system", "key": "event_key"}] * 4)
reject_saved("MetricExecute", "row budget", metric_execute, ["limit"], 501)
reject_saved("MetricExecute", "interval", metric_execute, ["interval"], "week")
reject_saved("MetricExecute", "scope override", metric_execute, ["environment"], "production")
reject_saved("MetricValidation", "execution performed", metric_validation, ["execution_performed"], True)
print(f"Independent metric contracts verified: {len(metric_cases)} cases and {metric_negative_count} unit incompatibilities, plus strict fields/runtime budgets.")

# Composite formula metrics reuse immutable metric references and a restricted
# typed expression. Exercise the generated bundle for every management shape.
formula_ref_a = {"metric_id": saved_id, "version": 1}
formula_ref_b = {"metric_id": "00000000-0000-4000-8000-000000000002", "version": 1}
formula_expr = {
    "op": "divide",
    "left": {"op": "metric", "metric": formula_ref_a},
    "right": {"op": "metric", "metric": formula_ref_b},
}
formula_formula = {"expression": formula_expr, "unit": "number"}
formula_definition = {"title": "Lesson ratio", "description": "two counts", "formula": formula_formula}
formula_dependency = {
    "project_id": saved_id,
    "environment": "development",
    "metrics": [
        {"metric_id": saved_id, "version": 1, "definition_hash": "a" * 64},
        {"metric_id": "00000000-0000-4000-8000-000000000002", "version": 1, "definition_hash": "b" * 64},
    ],
}
formula_record = {
    "metric_id": saved_id, "project_id": saved_id, "environment": "development",
    "title": "Lesson ratio", "draft_revision": 1, "published_version": 0,
    "created_at": "2026-11-10T00:00:00Z", "updated_at": "2026-11-10T00:00:00Z",
}
formula_validation = {
    "valid": True, "execution_performed": False, "draft_revision": 1,
    "published_version": 0, "normalized_definition": formula_definition,
    "dependencies": formula_dependency, "definition_hash": "c" * 64,
    "dependency_hash": "d" * 64,
    "diff": [{"category": "definition", "path": "/definition", "after": formula_definition}],
    "validation_hash": "e" * 64,
}
formula_version = {
    "metric_id": saved_id, "project_id": saved_id, "environment": "development",
    "version": 1, "draft_revision": 1, "definition": formula_definition,
    "dependencies": formula_dependency, "definition_hash": "c" * 64,
    "dependency_hash": "d" * 64, "validation_hash": "e" * 64,
    "created_at": "2026-11-10T00:00:00Z", "created_by": saved_id,
}
formula_cases = {
    "FormulaExpression": formula_expr,
    "FormulaDefinition": formula_formula,
    "FormulaMetricDefinition": formula_definition,
    "FormulaDependencyManifest": formula_dependency,
    "FormulaMetricRecord": formula_record,
    "FormulaMetricDraft": {**formula_record, "definition": formula_definition},
    "FormulaMetricVersion": formula_version,
    "FormulaMetricValidation": formula_validation,
    "FormulaMetricCreate": {"environment": "development", "definition": formula_definition},
    "FormulaMetricUpdate": {"definition": formula_definition},
    "FormulaMetricValidate": {"draft_revision": 1, "expected_published_version": 0},
    "FormulaMetricPublish": {"draft_revision": 1, "expected_published_version": 0, "validation_hash": "e" * 64},
    "FormulaMetricList": {"items": [formula_record], "next_cursor": None},
    "FormulaMetricVersionList": {"items": [formula_version], "next_cursor": None},
}
for name, value in formula_cases.items():
    validator(name).validate(value)
    if validator(name).is_valid({**value, "unexpected_field": True}):
        raise AssertionError(f"generated {name} accepted unknown field")

# Nested expression, constant scaling and nullable-free arithmetic are allowed.
nested_expr = {
    "op": "multiply",
    "left": {"op": "metric", "metric": formula_ref_a},
    "right": {"op": "add", "left": {"op": "constant", "value": 1}, "right": {"op": "metric", "metric": formula_ref_b}},
}
validator("FormulaExpression").validate(nested_expr)

formula_negative_count = 0
for label, path, value in (
    ("unknown op", ["op"], "pow"),
    ("missing op", ["op"], None),
    ("unsafe constant", ["right", "left", "value"], 9007199254740992),
    ("bad reference id", ["right", "right", "metric", "metric_id"], "not-a-uuid"),
    ("bad reference version", ["right", "right", "metric", "version"], 0),
):
    bad = copy.deepcopy(nested_expr)
    parent = bad
    for key in path[:-1]:
        parent = parent[key]
    if value is None:
        del parent[path[-1]]
    else:
        parent[path[-1]] = value
    if validator("FormulaExpression").is_valid(bad):
        raise AssertionError(f"FormulaExpression accepted invalid {label}")
    formula_negative_count += 1
for label, path, value in (
    ("atomic event_versions", ["event_versions"], []),
    ("atomic aggregation", ["aggregation"], {"op": "count"}),
    ("unbounded title", ["title"], "a" * 129),
    ("unbounded description", ["description"], "a" * 2001),
    ("bad formula unit", ["formula", "unit"], "parsec"),
    ("missing formula", ["formula"], None),
):
    reject_saved("FormulaMetricDefinition", label, formula_definition, path, value, remove=(value is None))
    formula_negative_count += 1
reject_saved("FormulaDefinition", "invalid unit", formula_formula, ["unit"], "parsec")
reject_saved("FormulaDependencyManifest", "empty metrics", formula_dependency, ["metrics"], [])
reject_saved("FormulaDependencyManifest", "too many metrics", formula_dependency, ["metrics"], formula_dependency["metrics"] * 5)
reject_saved("FormulaMetricValidation", "execution performed", formula_validation, ["execution_performed"], True)
reject_saved("FormulaMetricPublish", "malformed hash", formula_cases["FormulaMetricPublish"], ["validation_hash"], "invalid")
reject_saved("FormulaMetricList", "unbounded list", formula_cases["FormulaMetricList"], ["items"], [formula_record] * 101)
reject_saved("FormulaMetricVersionList", "unbounded version list", formula_cases["FormulaMetricVersionList"], ["items"], [formula_version] * 51)
print(f"Composite formula metric contracts verified: {len(formula_cases)} cases and {formula_negative_count} negative cases, plus recursive expression and strict field checks.")
