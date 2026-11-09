#!/usr/bin/env python3
"""Validate desired event data and compare complete membership with an active catalog."""
import argparse
import json
import re
import sys

from activity_api import MAX_BODY, SafeError, UUID, read_json


def check(condition):
    if not condition:
        raise SafeError("invalid desired event manifest")


def validate(value):
    check(isinstance(value, dict))
    check(set(value) == {"format", "project_id", "environment", "complete_release", "removed_event_keys", "events"})
    check(value["format"] == "activity.desired-events.v1" and value["complete_release"] is True)
    check(isinstance(value["project_id"], str) and re.fullmatch(UUID, value["project_id"]))
    check(value["environment"] in ("development", "staging", "production"))
    key_ok = lambda k: isinstance(k, str) and re.fullmatch(r"[a-z][a-z0-9_]{0,63}", k)
    removed = value["removed_event_keys"]
    check(isinstance(removed, list) and all(key_ok(k) for k in removed) and len(removed) == len(set(removed)))
    events = value["events"]
    check(isinstance(events, list) and 1 <= len(events) <= 200)
    keys = []
    for event in events:
        check(isinstance(event, dict))
        required = {"event_key", "display_name", "trigger", "allowed_source_kinds", "fact_kind", "schema", "example_properties", "implementation_sites"}
        check(required <= set(event) and not set(event) - required - {"field_metadata", "description"})
        check(key_ok(event["event_key"]))
        keys.append(event["event_key"])
        for field, limit in (("display_name", 128), ("trigger", 2000)):
            check(isinstance(event[field], str) and 1 <= len(event[field]) <= limit)
        if "description" in event:
            check(isinstance(event["description"], str) and len(event["description"]) <= 2000)
        kinds = event["allowed_source_kinds"]
        check(isinstance(kinds, list) and 1 <= len(kinds) <= 2 and all(k in ("browser", "server") for k in kinds) and len(set(kinds)) == len(kinds))
        check(event["fact_kind"] in ("user_intent", "client_observed", "server_confirmed"))
        check(isinstance(event["schema"], dict) and event["schema"].get("type") == "object" and event["schema"].get("additionalProperties") is False)
        check(isinstance(event["schema"].get("properties"), dict) and len(event["schema"]["properties"]) <= 50)
        check(all(key_ok(k) for k in event["schema"]["properties"]))
        check(isinstance(event["example_properties"], dict) and len(event["example_properties"]) <= 50)
        if "field_metadata" in event:
            check(isinstance(event["field_metadata"], dict) and len(event["field_metadata"]) <= 50)
        sites = event["implementation_sites"]
        check(isinstance(sites, list) and 1 <= len(sites) <= 100)
        for site in sites:
            check(isinstance(site, dict) and set(site) == {"path", "reason"})
            path = site["path"]
            check(isinstance(path, str) and 0 < len(path) <= 512 and not path.startswith(("/", "~")) and "\\" not in path and not any(p in ("", ".", "..") for p in path.split("/")))
            check(isinstance(site["reason"], str) and 1 <= len(site["reason"]) <= 2000)
    check(len(keys) == len(set(keys)) and not set(keys) & set(removed))
    normalized = json.loads(json.dumps(value, allow_nan=False))
    normalized["events"].sort(key=lambda e: e["event_key"])
    normalized["removed_event_keys"].sort()
    for event in normalized["events"]:
        event["allowed_source_kinds"].sort()
        if isinstance(event["schema"].get("required"), list):
            event["schema"]["required"].sort()
    return normalized


def membership_diff(manifest, snapshot):
    check(isinstance(snapshot, dict) and set(snapshot) == {"project_id", "environment", "active_release_revision", "items"})
    check(snapshot["project_id"] == manifest["project_id"] and snapshot["environment"] == manifest["environment"])
    check(type(snapshot["active_release_revision"]) is int and snapshot["active_release_revision"] >= 0)
    check(isinstance(snapshot["items"], list))
    active = set()
    for item in snapshot["items"]:
        check(isinstance(item, dict) and isinstance(item.get("event_key"), str) and type(item.get("active")) is bool)
        if item["active"]:
            check(item["event_key"] not in active)
            active.add(item["event_key"])
    desired = {e["event_key"] for e in manifest["events"]}
    removed = sorted(active - desired)
    check(removed == manifest["removed_event_keys"])
    return {"active_release_revision": snapshot["active_release_revision"],
            "added": sorted(desired - active), "retained_requires_definition_comparison": sorted(desired & active),
            "removed": removed, "complete_release": True}


def load(path):
    with open(path, "rb") as handle:
        raw = handle.read(MAX_BODY + 1)
    check(len(raw) <= MAX_BODY)
    return read_json(raw)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest")
    parser.add_argument("--active-snapshot", help="locally assembled complete API catalog snapshot; not an API endpoint")
    args = parser.parse_args(argv)
    try:
        manifest = validate(load(args.manifest))
        result = membership_diff(manifest, load(args.active_snapshot)) if args.active_snapshot else {"structurally_valid": True, "events": len(manifest["events"]), "server_validation_required": True}
        print(json.dumps(result, sort_keys=True, separators=(",", ":")))
        return 0
    except (SafeError, OSError, ValueError, TypeError, RecursionError):
        print("Manifest validation failed; no manifest data printed.", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
