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

print(
    f"OpenAPI and JSON Schemas verified; {len(fixtures)} shared fixtures, "
    f"{len(journey['items'])} journey events, {len(system['events'])} system "
    "property examples and three negative cases passed."
)
