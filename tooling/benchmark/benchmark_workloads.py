"""Deterministic workload requests and semantic result expectations."""
from collections import Counter
from datetime import timedelta
import hashlib
import uuid

from benchmark_common import parse_time, timestamp, delta_ns, require, SmokeFailure

def uid(config, ordinal=0):
    return "bench-" + config["run_id"] + "-" + str(ordinal % config["uid_cardinality"])


def event_id(config, ordinal):
    digest = bytearray(hashlib.sha256(f'{config["run_id"]}/{config["source_kind"]}/event/{ordinal}'.encode()).digest()[:16])
    digest[6] = (digest[6] & 0x0f) | 0x40
    digest[8] = (digest[8] & 0x3f) | 0x80
    return str(uuid.UUID(bytes=bytes(digest)))


def property_field(config, key):
    return {"kind": "property", "event_key": config["event_key"], "schema_version": config["schema_version"], "key": key}


def request_base(config):
    started = parse_time(config["started_at"])
    return {"kind": "trend", "environment": config["environment"], "timezone": "UTC", "subject": "uid",
            "time_range": {"from": timestamp(started - timedelta(seconds=1801)), "to": timestamp(started + timedelta(seconds=1))},
            "event_versions": [{"event_key": config["event_key"], "schema_version": config["schema_version"]}],
            "metrics": [{"op": "count", "accuracy": "exact"}], "limit": 500}


def workloads(config):
    base = request_base(config)
    exact_uid = {**base, "metrics": [{"op": "unique", "accuracy": "exact"}]}
    journey = {key: value for key, value in base.items() if key != "metrics"}
    journey.update(kind="journey", identity_filter={"uid": uid(config)})
    funnel = {key: value for key, value in journey.items() if key != "event_versions"}
    funnel.update(kind="funnel", funnel={"mode": "ordered_with_interleaving", "window_seconds": 1800,
        "steps": [{"event_key": config["event_key"], "schema_version": config["schema_version"]} for _ in range(2)]})
    return {"count": base, "exact_uid": exact_uid, "hour_bucket": {**base, "interval": "hour"},
            "section_group": {**base, "group_by": [property_field(config, "section")]},
            "high_cardinality_uid_filter": {**base, "filter": {"op": "eq", "field": {"kind": "system", "key": "uid"}, "value": uid(config)}},
            "property_filter": {**base, "filter": {"op": "gte", "field": property_field(config, "duration_ms"), "value": 900}},
            "journey": journey, "ordered_funnel": funnel}


def matching_modulo(events, modulus, remainder=0):
    return max(0, (events - 1 - remainder) // modulus + 1)


def validate_result(name, result, config, events):
    rows = result["rows"]
    metadata = result.get("metadata", {})
    require(metadata.get("coverage", {}).get("state") == "provisional", "Coverage must remain honestly provisional")
    if name not in ("journey", "ordered_funnel"):
        require(metadata.get("accuracy") == "exact", "Aggregate result must be exact")
    if name in ("count", "exact_uid", "high_cardinality_uid_filter", "property_filter"):
        expected = {"count": events, "exact_uid": min(events, config["uid_cardinality"]),
                    "high_cardinality_uid_filter": matching_modulo(events, config["uid_cardinality"]),
                    "property_filter": events // 1000 * 100 + max(0, events % 1000 - 900)}[name]
        require(len(rows) == 1 and rows[0].get("metric_0") == expected, name + " did not match deterministic fixture")
    elif name == "section_group":
        expected = {"section-" + str(i): matching_modulo(events, 10, i) for i in range(min(events, 10))}
        actual = {row.get("group_0"): row.get("metric_0") for row in rows}
        require(len(rows) == len(expected) and actual == expected and all(row.get("group_0_present") is True for row in rows),
                "Section groups did not match deterministic fixture")
    elif name == "hour_bucket":
        anchor = parse_time(config["started_at"]) - timedelta(seconds=1800)
        # Count ordinal ranges analytically; do not materialize a million fixtures
        # on every query sample. Go generates floor(i*1800e9/events) nanoseconds.
        expected = Counter()
        bucket = anchor.replace(minute=0, second=0, microsecond=0)
        while bucket < anchor + timedelta(seconds=1800):
            lower_ns = max(0, delta_ns(bucket-anchor))
            upper_ns = min(1_800_000_000_000, delta_ns(bucket+timedelta(hours=1)-anchor))
            first = (lower_ns * events + 1_800_000_000_000 - 1) // 1_800_000_000_000
            last = (upper_ns * events + 1_800_000_000_000 - 1) // 1_800_000_000_000
            if last > first:
                expected[bucket] = last-first
            bucket += timedelta(hours=1)
        actual = {parse_time(row["bucket"]): row.get("metric_0") for row in rows}
        require(len(rows) == len(expected) and actual == dict(expected), "Time buckets did not match deterministic fixture")
    elif name == "journey":
        count = matching_modulo(events, config["uid_cardinality"])
        require(count <= 500, "Journey expectation exceeds snapshot row budget")
        require(len(rows) == count and all(row.get("uid") == uid(config) for row in rows), "Journey crossed UID boundary or lost facts")
        require(len({(row.get("source_id"), row.get("event_id")) for row in rows}) == count, "Journey retained duplicate source/event pairs")
        require([row.get("event_id") for row in rows] == [event_id(config, i) for i in range(0, events, config["uid_cardinality"])],
                "Journey IDs or deterministic event ordering changed")
        if config.get("source_id"):
            require(all(row.get("source_id") == config["source_id"] for row in rows), "Journey crossed source boundary")
        require(all(row.get("properties", {}).get("post_id") == "post-" + str(i % 10000)
                    for row, i in zip(rows, range(0, events, config["uid_cardinality"]))), "Journey ordering or properties changed")
    elif name == "ordered_funnel":
        visits = matching_modulo(events, config["uid_cardinality"])
        require(len(rows) == 2 and [row.get("step_index") for row in rows] == [0, 1]
                and [row.get("subjects") for row in rows] == [int(visits >= 1), int(visits >= 2)],
                "Ordered repeat-visit funnel did not match known UID sequence")
    else:
        raise SmokeFailure("Unknown workload validator")
    return len(rows)


