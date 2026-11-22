#!/usr/bin/env python3
"""Reconcile synthetic session-replay capacity reports and apply fixed gates.

This module does not collect events, call Evia, or manage workers. The CLI only
reads one private summarized report and creates one private evaluation file.
"""
import argparse
import json
import os
from pathlib import Path
import re
import sys

from benchmark_common import SmokeFailure, loopback, private_path, require

REPORT_VERSION = 2
REPORT_KIND = "evia_session_replay_capacity"
MIB = 1024 * 1024
MIN_CLICKHOUSE_DISK_RESERVE_BYTES = 2 * 1024 * MIB
ENCODED_CHARACTER_BUDGETS = (2 * MIB, 8 * MIB)
PROFILES = ("baseline", "weak")
EXPECTED_SCENARIOS = tuple(
    (profile, budget) for profile in PROFILES for budget in ENCODED_CHARACTER_BUDGETS
)
NAMESPACE = re.compile(r"evia_bench_[a-f0-9]{12}\Z")
SHA256 = re.compile(r"[a-f0-9]{64}\Z")
PRIVACY_SCAN_LOCATIONS = (
    "browser_request_properties",
    "clickhouse_properties",
    "artifact_plaintext",
)
MIN_LATENCY_SAMPLES = 30
MIN_RSS_SAMPLES = 30

# These are initial acceptance gates, not measured results. They are deliberately
# fixed here so a report cannot choose looser limits after a run.
PROFILE_LIMITS = {
    "baseline": {
        "collect_ack_p95_ms": 2_000,
        "artifact_ready_p95_ms": 120_000,
        "owner_page_p95_ms": 2_000,
        "machine_page_p95_ms": 2_000,
        "worker_transition_ms": 120_000,
        "api_peak_rss_bytes": 2 * 1024 * MIB,
        "content_worker_peak_rss_bytes": 2 * 1024 * MIB,
    },
    "weak": {
        "collect_ack_p95_ms": 5_000,
        "artifact_ready_p95_ms": 300_000,
        "owner_page_p95_ms": 5_000,
        "machine_page_p95_ms": 5_000,
        "worker_transition_ms": 300_000,
        "api_peak_rss_bytes": 3 * 1024 * MIB,
        "content_worker_peak_rss_bytes": 3 * 1024 * MIB,
    },
}


def dedicated_namespace(value):
    """Return a valid benchmark namespace or fail without touching dependencies."""
    require(isinstance(value, str) and NAMESPACE.fullmatch(value) is not None,
            "Replay capacity requires a fresh dedicated benchmark namespace")
    return value


def _mapping(value, path, failures):
    if not isinstance(value, dict):
        failures.append(path + " must be an object")
        return {}
    return value


def _integer(mapping, key, path, failures, minimum=0):
    value = mapping.get(key)
    if type(value) is not int or value < minimum:
        failures.append(f"{path}.{key} must be an integer >= {minimum}")
        return 0
    return value


def _boolean(mapping, key, path, failures):
    value = mapping.get(key)
    if type(value) is not bool:
        failures.append(f"{path}.{key} must be a boolean")
        return False
    return value


def _nonempty_string(mapping, key, path, failures):
    value = mapping.get(key)
    if not isinstance(value, str) or not value:
        failures.append(f"{path}.{key} must be a non-empty string")
        return ""
    return value


def _utc_timestamp(mapping, key, path, failures):
    value = mapping.get(key)
    if not isinstance(value, str) or not value.endswith("Z"):
        failures.append(f"{path}.{key} must be a UTC timestamp string")
        return ""
    return value


def _distribution(latency, name, limit, failures):
    path = "latency." + name
    observed = _mapping(latency.get(name), path, failures)
    samples = _integer(observed, "samples", path, failures, 1)
    p95 = _integer(observed, "p95_ms", path, failures)
    maximum = _integer(observed, "max_ms", path, failures)
    if samples < MIN_LATENCY_SAMPLES:
        failures.append(f"{path}.samples must be >= {MIN_LATENCY_SAMPLES}")
    if p95 > maximum:
        failures.append(f"{path}.p95_ms must not exceed max_ms")
    if p95 > limit:
        failures.append(f"{path}.p95_ms exceeded the fixed {limit} ms gate")
    return {"samples": samples, "p95_ms": p95, "max_ms": maximum, "limit_ms": limit}


def _principal_page(pagination, principal, expected_items, frozen_at, failures):
    path = "pagination." + principal
    page = _mapping(pagination.get(principal), path, failures)
    expected_auth = "owner_session" if principal == "owner" else "machine_credential"
    if page.get("auth_kind") != expected_auth:
        failures.append(f"{path}.auth_kind must be {expected_auth}")
    if _integer(page, "status_code", path, failures) != 200:
        failures.append(f"{path}.status_code must be 200")
    pages = _integer(page, "pages", path, failures, 1)
    items = _integer(page, "items", path, failures)
    distinct = _integer(page, "distinct_items", path, failures)
    duplicates = _integer(page, "duplicate_items", path, failures)
    missing = _integer(page, "missing_items", path, failures)
    cursor_errors = _integer(page, "cursor_errors", path, failures)
    digest = page.get("item_set_sha256")
    first_page_frozen_at = _utc_timestamp(
        page, "first_page_frozen_at", path, failures
    )
    as_of_values = page.get("as_of_values")
    if pages < 2:
        failures.append(f"{path}.pages must exercise at least two pages")
    if items != expected_items or distinct != expected_items:
        failures.append(f"{path} item counts must equal the frozen expected item count")
    if duplicates != 0 or missing != 0 or cursor_errors != 0:
        failures.append(f"{path} must have no duplicates, missing items, or cursor errors")
    if not isinstance(digest, str) or SHA256.fullmatch(digest) is None:
        failures.append(f"{path}.item_set_sha256 must be a lowercase SHA-256 digest")
        digest = ""
    if first_page_frozen_at != frozen_at:
        failures.append(f"{path}.first_page_frozen_at must equal pagination.frozen_at")
    if as_of_values != [frozen_at]:
        failures.append(f"{path}.as_of_values must contain only the frozen as_of value")
    return {
        "items": items,
        "distinct_items": distinct,
        "item_set_sha256": digest,
        "first_page_frozen_at": first_page_frozen_at,
    }


def reconcile_scenario(scenario):
    """Purely reconcile one scenario and return its observed summary and failures."""
    failures = []
    scenario = _mapping(scenario, "scenario", failures)
    profile = scenario.get("profile")
    budget = scenario.get("budget_encoded_chars")
    if profile not in PROFILES:
        failures.append("profile must be baseline or weak")
    if type(budget) is not int or budget not in ENCODED_CHARACTER_BUDGETS:
        failures.append("budget_encoded_chars must be exactly 2 MiB or 8 MiB encoded characters")
        budget = 0

    browser = _mapping(scenario.get("browser"), "browser", failures)
    clickhouse = _mapping(scenario.get("clickhouse"), "clickhouse", failures)
    postgres = _mapping(scenario.get("postgres"), "postgres", failures)

    counts = {
        "browser_accepted_chunks": _integer(browser, "accepted_chunks", "browser", failures),
        "clickhouse_distinct_chunks": _integer(clickhouse, "distinct_chunks", "clickhouse", failures),
        "postgres_chunks": _integer(postgres, "chunks", "postgres", failures),
        "browser_accepted_items": _integer(browser, "accepted_items", "browser", failures),
        "clickhouse_distinct_items": _integer(clickhouse, "distinct_items", "clickhouse", failures),
        "postgres_items": _integer(postgres, "items", "postgres", failures),
        "postgres_artifacts": _integer(postgres, "artifacts", "postgres", failures),
        "clickhouse_raw_items": _integer(clickhouse, "raw_items", "clickhouse", failures),
        "clickhouse_incomplete_raw_items": _integer(
            clickhouse, "incomplete_raw_items", "clickhouse", failures
        ),
        "postgres_raw_items": _integer(postgres, "raw_items", "postgres", failures),
        "postgres_incomplete_raw_items": _integer(
            postgres, "incomplete_raw_items", "postgres", failures
        ),
    }
    # The protected artifact store writes one immutable object per decrypted
    # chunk; item assembly is a read-time operation, not another artifact file.
    chunk_counts = [counts[key] for key in (
        "browser_accepted_chunks", "clickhouse_distinct_chunks", "postgres_chunks",
        "postgres_artifacts")]
    item_counts = [counts[key] for key in (
        "browser_accepted_items", "clickhouse_distinct_items", "postgres_items")]
    if chunk_counts[0] <= 0 or len(set(chunk_counts)) != 1:
        failures.append("browser accepted, ClickHouse distinct, PostgreSQL chunk, and protected artifact counts must be positive and equal")
    if item_counts[0] <= 0 or len(set(item_counts)) != 1:
        failures.append("browser accepted, ClickHouse distinct, and PostgreSQL complete item counts must be positive and equal")
    if (counts["clickhouse_raw_items"]
            != counts["clickhouse_distinct_items"] + counts["clickhouse_incomplete_raw_items"]):
        failures.append("ClickHouse raw items must equal complete plus incomplete raw items")
    if (counts["postgres_raw_items"]
            != counts["postgres_items"] + counts["postgres_incomplete_raw_items"]):
        failures.append("PostgreSQL raw items must equal complete plus incomplete raw items")
    if (counts["clickhouse_raw_items"] != counts["postgres_raw_items"]
            or counts["clickhouse_incomplete_raw_items"]
            != counts["postgres_incomplete_raw_items"]):
        failures.append("ClickHouse and PostgreSQL raw/incomplete item counts must agree")

    if _integer(browser, "receipt_rejections", "browser", failures) != 0:
        failures.append("browser.receipt_rejections must be zero")
    if _integer(browser, "unknown_outcomes", "browser", failures) != 0:
        failures.append("browser.unknown_outcomes must be zero")
    statuses = _mapping(browser.get("collect_status_counts"), "browser.collect_status_counts", failures)
    if not statuses or set(statuses) != {"202"} or type(statuses.get("202")) is not int or statuses.get("202", 0) <= 0:
        failures.append("browser.collect_status_counts must contain positive 202 responses only")

    encoded_total = _integer(browser, "accepted_encoded_chars_total", "browser", failures, 1)
    gzip_total = _integer(browser, "accepted_gzip_bytes_total", "browser", failures, 1)
    max_session = _integer(browser, "max_session_encoded_chars", "browser", failures, 1)
    largest_rejected = _integer(
        browser, "largest_attempted_rejected_payload_encoded_chars", "browser", failures, 1
    )
    boundary_sessions = _integer(browser, "budget_boundary_reached_sessions", "browser", failures, 1)
    overshoot_sessions = _integer(browser, "budget_overshoot_sessions", "browser", failures)
    budget_checkpoint = _mapping(
        browser.get("budget_checkpoint"), "browser.budget_checkpoint", failures
    )
    checkpoint_accepted = _integer(
        budget_checkpoint, "accepted_encoded_chars", "browser.budget_checkpoint", failures, 1
    )
    checkpoint_reserved = _integer(
        budget_checkpoint, "reserved_encoded_chars", "browser.budget_checkpoint", failures
    )
    checkpoint_pending = _integer(
        budget_checkpoint, "pending_encoded_chars", "browser.budget_checkpoint", failures
    )
    if budget and max_session > budget:
        failures.append("browser.max_session_encoded_chars exceeded budget_encoded_chars")
    if checkpoint_accepted != max_session:
        failures.append("browser.budget_checkpoint.accepted_encoded_chars must equal max_session_encoded_chars")
    if checkpoint_reserved != 0 or checkpoint_pending != 0:
        failures.append("browser budget checkpoint must have zero reserved and pending encoded characters")
    if budget and checkpoint_accepted + largest_rejected <= budget:
        failures.append("encoded-character boundary was not exercised by the largest attempted rejected payload")
    if overshoot_sessions != 0:
        failures.append("browser.budget_overshoot_sessions must be zero")

    capture = _mapping(scenario.get("capture"), "capture", failures)
    capture_wall_ms = _integer(capture, "wall_time_ms", "capture", failures, 1)
    if not _boolean(capture, "pagehide_triggered", "capture", failures):
        failures.append("capture.pagehide_triggered must be true")
    if not _boolean(capture, "pagehide_finalized", "capture", failures):
        failures.append("capture.pagehide_finalized must be true")
    if not _boolean(capture, "pagehide_finalized_before_context_close", "capture", failures):
        failures.append("capture.pagehide must finalize before browser context close")

    privacy = _mapping(scenario.get("privacy"), "privacy", failures)
    canary_count = _integer(privacy, "canary_count", "privacy", failures, 2)
    injection = _mapping(
        privacy.get("canary_injection"), "privacy.canary_injection", failures
    )
    for key in ("password_input_injected", "masked_region_injected", "injected_before_capture"):
        if not _boolean(injection, key, "privacy.canary_injection", failures):
            failures.append(f"privacy.canary_injection.{key} must be true")
    matches = _mapping(privacy.get("canary_matches"), "privacy.canary_matches", failures)
    if set(matches) != set(PRIVACY_SCAN_LOCATIONS):
        failures.append("privacy.canary_matches must cover the fixed browser, ClickHouse, and artifact locations")
    for location in PRIVACY_SCAN_LOCATIONS:
        if type(matches.get(location)) is not int or matches.get(location) != 0:
            failures.append(f"privacy.canary_matches.{location} must be zero")
    if _integer(clickhouse, "plaintext_content_rows", "clickhouse", failures) != 0:
        failures.append("clickhouse.plaintext_content_rows must be zero")
    disk_before = _integer(
        clickhouse, "disk_reserve_bytes_before", "clickhouse", failures, 1
    )
    disk_after = _integer(
        clickhouse, "disk_reserve_bytes_after", "clickhouse", failures, 1
    )
    if min(disk_before, disk_after) < MIN_CLICKHOUSE_DISK_RESERVE_BYTES:
        failures.append(
            "clickhouse disk reserve fell below the fixed 2 GiB observation gate"
        )

    worker = _mapping(scenario.get("worker"), "worker", failures)
    pending_after = _integer(worker, "pending_after_recovery", "worker", failures)
    duplicate_chunks = _integer(worker, "duplicate_chunks_after_recovery", "worker", failures)
    transition_ms = _integer(worker, "transition_ms", "worker", failures)
    generation_before = _nonempty_string(
        worker, "generation_before_transition", "worker", failures
    )
    generation_after = _nonempty_string(
        worker, "generation_after_recovery", "worker", failures
    )
    restarts_before = _integer(
        worker, "restart_count_before_transition", "worker", failures
    )
    restarts_after = _integer(
        worker, "restart_count_after_recovery", "worker", failures
    )
    restart_count = _integer(worker, "restart_count", "worker", failures)
    if restarts_after < restarts_before:
        failures.append("worker restart checkpoint count must not regress")
    elif restart_count != restarts_after - restarts_before:
        failures.append("worker.restart_count must equal the restart checkpoint delta")
    if pending_after != 0 or duplicate_chunks != 0:
        failures.append("worker recovery must leave zero pending and duplicate chunks")
    if profile == "baseline":
        if _integer(worker, "pending_before_drain", "worker", failures, 1) <= 0:
            failures.append("baseline must begin graceful drain with pending chunks")
        if not _boolean(worker, "graceful_drain_completed", "worker", failures):
            failures.append("baseline graceful drain must complete")
        if restart_count != 0 or generation_after != generation_before:
            failures.append("baseline must not restart or change content-worker generation")
    elif profile == "weak":
        if _integer(worker, "pending_before_restart", "worker", failures, 1) <= 0:
            failures.append("weak scenario must restart with pending chunks")
        if restart_count < 1 or generation_after == generation_before:
            failures.append("weak scenario must restart into a new content-worker generation")
        if not _boolean(worker, "restart_recovery_completed", "worker", failures):
            failures.append("weak restart recovery must complete")

    pagination = _mapping(scenario.get("pagination"), "pagination", failures)
    frozen_at = _utc_timestamp(pagination, "frozen_at", "pagination", failures)
    appended = _integer(pagination, "appended_after_freeze_items", "pagination", failures, 1)
    frozen_expected = _integer(pagination, "frozen_expected_items", "pagination", failures, 1)
    excluded_incomplete = _integer(
        pagination, "discovery_excluded_incomplete_items", "pagination", failures
    )
    shared_freeze = _boolean(
        pagination, "shared_first_page_freeze", "pagination", failures
    )
    if frozen_expected + appended != counts["postgres_items"]:
        failures.append("frozen pagination count plus post-freeze complete items must equal final PostgreSQL complete items")
    if excluded_incomplete != counts["postgres_incomplete_raw_items"]:
        failures.append("discovery must exclude every incomplete raw item")
    owner = _principal_page(pagination, "owner", frozen_expected, frozen_at, failures)
    machine = _principal_page(pagination, "machine", frozen_expected, frozen_at, failures)
    if (not shared_freeze
            or owner["first_page_frozen_at"] != machine["first_page_frozen_at"]):
        failures.append("owner and machine first pages must attest one shared freeze timestamp")
    if owner["item_set_sha256"] != machine["item_set_sha256"]:
        failures.append("owner and machine frozen item sets must be identical")

    limits = PROFILE_LIMITS.get(profile, PROFILE_LIMITS["baseline"])
    latency = _mapping(scenario.get("latency"), "latency", failures)
    latency_summary = {
        name: _distribution(latency, name, limits[name + "_p95_ms"], failures)
        for name in ("collect_ack", "artifact_ready", "owner_page", "machine_page")
    }
    if transition_ms > limits["worker_transition_ms"]:
        failures.append("worker.transition_ms exceeded the fixed profile gate")

    resources = _mapping(scenario.get("resources"), "resources", failures)
    resource_summary = {
        key: _integer(resources, key, "resources", failures, 1)
        for key in ("api_cpu_millicores", "api_memory_bytes",
                    "content_worker_cpu_millicores", "content_worker_memory_bytes")
    }

    rss = _mapping(scenario.get("rss"), "rss", failures)
    rss_samples = _integer(rss, "samples", "rss", failures, 1)
    api_rss = _integer(rss, "api_peak_bytes", "rss", failures, 1)
    worker_rss = _integer(rss, "content_worker_peak_bytes", "rss", failures, 1)
    if rss_samples < MIN_RSS_SAMPLES:
        failures.append(f"rss.samples must be >= {MIN_RSS_SAMPLES}")
    if api_rss > limits["api_peak_rss_bytes"]:
        failures.append("rss.api_peak_bytes exceeded the fixed profile gate")
    if worker_rss > limits["content_worker_peak_rss_bytes"]:
        failures.append("rss.content_worker_peak_bytes exceeded the fixed profile gate")

    return {
        "profile": profile,
        "budget_encoded_chars": budget,
        "passed": not failures,
        "failures": failures,
        "reconciliation": counts,
        "volume_units": {
            "accepted_encoded_chars_total": encoded_total,
            "accepted_gzip_bytes_total": gzip_total,
            "max_session_encoded_chars": max_session,
            "budget_checkpoint": {
                "accepted_encoded_chars": checkpoint_accepted,
                "reserved_encoded_chars": checkpoint_reserved,
                "pending_encoded_chars": checkpoint_pending,
            },
            "largest_attempted_rejected_payload_encoded_chars": largest_rejected,
            "budget_boundary_reached_sessions": boundary_sessions,
        },
        "capture": {
            "wall_time_ms": capture_wall_ms,
            "report_only": True,
            "pagehide_finalized_before_context_close": capture.get(
                "pagehide_finalized_before_context_close"
            ) is True,
        },
        "clickhouse_disk_reserve": {
            "before_bytes": disk_before,
            "after_bytes": disk_after,
            "minimum_bytes": MIN_CLICKHOUSE_DISK_RESERVE_BYTES,
        },
        "latency": latency_summary,
        "rss": {
            "samples": rss_samples,
            "api_peak_bytes": api_rss,
            "content_worker_peak_bytes": worker_rss,
        },
        "limits": dict(limits),
        "resources": resource_summary,
        "privacy_canaries": canary_count,
    }


def evaluate_report(report):
    """Purely evaluate a summarized report; malformed or missing evidence fails closed."""
    global_failures = []
    report = _mapping(report, "report", global_failures)
    if report.get("report_version") != REPORT_VERSION:
        global_failures.append(f"report_version must be {REPORT_VERSION}")
    if report.get("kind") != REPORT_KIND:
        global_failures.append("kind must identify the session replay capacity report")
    if report.get("synthetic") is not True:
        global_failures.append("synthetic must be true")
    if report.get("production_writes") is not False:
        global_failures.append("production_writes must be false")
    if report.get("environment") != "development":
        global_failures.append("environment must be development")
    try:
        api_url = loopback(report.get("api_url"))
    except (SmokeFailure, TypeError):
        api_url = ""
        global_failures.append("api_url must be a literal loopback HTTP origin")
    try:
        namespace = dedicated_namespace(report.get("namespace"))
    except SmokeFailure:
        namespace = ""
        global_failures.append("namespace must be a dedicated synthetic benchmark namespace")

    scenarios_value = report.get("scenarios")
    if not isinstance(scenarios_value, list):
        global_failures.append("scenarios must be an array")
        scenarios_value = []
    results = [reconcile_scenario(scenario) for scenario in scenarios_value]
    keys = [(result["profile"], result["budget_encoded_chars"]) for result in results]
    if len(keys) != len(EXPECTED_SCENARIOS) or set(keys) != set(EXPECTED_SCENARIOS):
        global_failures.append("scenario matrix must contain each baseline/weak x 2 MiB/8 MiB encoded-character case exactly once")
    if len(keys) != len(set(keys)):
        global_failures.append("scenario matrix contains duplicate profile/budget cases")
    by_key = {(result["profile"], result["budget_encoded_chars"]): result for result in results}
    for budget in ENCODED_CHARACTER_BUDGETS:
        baseline = by_key.get(("baseline", budget))
        weak = by_key.get(("weak", budget))
        if baseline is None or weak is None:
            continue
        baseline_resources = baseline["resources"]
        weak_resources = weak["resources"]
        if (any(weak_resources[key] > baseline_resources[key] for key in baseline_resources)
                or not any(weak_resources[key] < baseline_resources[key] for key in baseline_resources)):
            global_failures.append(
                f"weak {budget}-encoded-character resources must be no larger than baseline and lower in at least one dimension")

    passed = not global_failures and all(result["passed"] for result in results)
    return {
        "evaluation_version": 2,
        "report_kind": REPORT_KIND,
        "passed": passed,
        "global_failures": global_failures,
        "api_url": api_url,
        "namespace": namespace,
        "budget_unit": "encoded_base64url_characters",
        "gzip_volume_unit": "bytes",
        "scenarios": results,
    }


def _load_private_report(value):
    path = private_path(value, existing=True)
    try:
        report = json.loads(path.read_text(encoding="utf-8"))
    except (ValueError, UnicodeError):
        raise SmokeFailure("Private replay capacity report is invalid JSON") from None
    require(isinstance(report, dict), "Private replay capacity report must be an object")
    return path, report


def _write_private_output(value, evaluation):
    path = private_path(value)
    require(not path.exists(), "Use a fresh private replay capacity output path")
    encoded = json.dumps(evaluation, indent=2, sort_keys=True, allow_nan=False) + "\n"
    flags = os.O_CREAT | os.O_EXCL | os.O_WRONLY
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    descriptor = os.open(path, flags, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
        stream.write(encoded)
    return path


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--api-url", required=True, help="Literal loopback HTTP origin used by the summarized run")
    parser.add_argument("--namespace", required=True, help="Dedicated evia_bench_<12 hex> namespace")
    parser.add_argument("--input", required=True, help="Mode-0600 summarized JSON report under /tmp")
    parser.add_argument("--out", required=True, help="Fresh private evaluation path under /tmp")
    args = parser.parse_args(argv)

    # Validate the execution boundary before reading a report or creating output.
    expected_api = loopback(args.api_url)
    expected_namespace = dedicated_namespace(args.namespace)
    _, report = _load_private_report(args.input)
    require(report.get("api_url") == expected_api, "CLI and report loopback origins differ")
    require(report.get("namespace") == expected_namespace, "CLI and report benchmark namespaces differ")
    evaluation = evaluate_report(report)
    _write_private_output(args.out, evaluation)
    print(json.dumps({"stage": "evaluated", "passed": evaluation["passed"],
                      "private_output_written": True}))
    return 0 if evaluation["passed"] else 1


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (SmokeFailure, OSError, ValueError, KeyError, TypeError) as error:
        reason = str(error) if isinstance(error, SmokeFailure) else "local validation failure"
        print(json.dumps({"stage": "failed", "reason": reason}), file=sys.stderr)
        raise SystemExit(2)
