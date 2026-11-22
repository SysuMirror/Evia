#!/usr/bin/env python3
"""Run a safe, resumable four-cell session-replay fixture harness.

The caller supplies executable hook commands for a dedicated loopback full-stack
fixture. Hooks receive only synthetic cell metadata and the private settings path;
they must emit bounded JSON observations. This tool never calls production
endpoints and does not provide an ordinary newSSE development-mode fallback.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile

from benchmark_common import SmokeFailure, loopback, private_path, require
from session_replay_capacity import (
    EXPECTED_SCENARIOS,
    REPORT_KIND,
    REPORT_VERSION,
    dedicated_namespace,
    evaluate_report,
    reconcile_scenario,
)

HARNESS_VERSION = 1
SETTINGS_KIND = "evia_session_replay_harness"
CELL_REPORT_KIND = "evia_session_replay_capacity_cell"
FIXTURE_KIND = "full_stack_new_sse"
NEW_SSE_MODE = "replay_capacity_fixture"
HOOK_NAMES = (
    "prepare_cell",
    "worker_hold",
    "browser_workload",
    "worker_drain",
    "worker_restart",
    "frozen_pagination",
    "reconcile",
)
RESOURCE_KEYS = (
    "api_cpu_millicores",
    "api_memory_bytes",
    "content_worker_cpu_millicores",
    "content_worker_memory_bytes",
)
NETWORK_PROFILES = {
    "baseline": {
        "latency_ms": 0,
        "download_bytes_per_second": -1,
        "upload_bytes_per_second": -1,
        "packet_loss_percent": 0,
    },
    "weak": {
        "latency_ms": 250,
        "download_bytes_per_second": 32 * 1024,
        "upload_bytes_per_second": 32 * 1024,
        "packet_loss_percent": 0,
    },
}
MAX_HOOK_OUTPUT_BYTES = 64 * 1024


def _private_directory(value):
    path = Path(value).expanduser().absolute()
    require(path.parent.resolve().is_relative_to(Path("/tmp")),
            "Replay cell report directory must live under /tmp")
    require(not path.is_symlink(), "Replay cell report directory must not be a symlink")
    mode = path.stat()
    require(stat.S_ISDIR(mode.st_mode) and stat.S_IMODE(mode.st_mode) == 0o700,
            "Replay cell report directory must be an existing mode 0700 directory")
    return path


def _command(value, name):
    require(isinstance(value, list) and bool(value)
            and all(isinstance(part, str) and part and "\x00" not in part for part in value),
            f"commands.{name} must be a non-empty argument array")
    executable = Path(value[0])
    require(executable.is_absolute(), f"commands.{name} executable must be absolute")
    try:
        mode = executable.stat()
    except OSError:
        raise SmokeFailure(f"commands.{name} executable is unavailable") from None
    require(stat.S_ISREG(mode.st_mode) and os.access(executable, os.X_OK),
            f"commands.{name} executable must be a regular executable file")
    return tuple(value)


def _resources(value):
    require(isinstance(value, dict) and set(value) == {"baseline", "weak"},
            "resources must contain baseline and weak profiles")
    result = {}
    for profile in ("baseline", "weak"):
        profile_value = value.get(profile)
        require(isinstance(profile_value, dict) and set(profile_value) == set(RESOURCE_KEYS),
                f"resources.{profile} must contain the four fixed resource dimensions")
        result[profile] = {}
        for key in RESOURCE_KEYS:
            observed = profile_value.get(key)
            require(type(observed) is int and observed > 0,
                    f"resources.{profile}.{key} must be a positive integer")
            result[profile][key] = observed
    baseline = result["baseline"]
    weak = result["weak"]
    require(all(weak[key] <= baseline[key] for key in RESOURCE_KEYS)
            and any(weak[key] < baseline[key] for key in RESOURCE_KEYS),
            "Weak resources must not exceed baseline and must lower at least one dimension")
    return result


def load_settings(value):
    """Load and fail-closed validate one private harness settings file."""
    path = private_path(value, existing=True)
    raw = path.read_bytes()
    try:
        settings = json.loads(raw, parse_constant=lambda value: (_ for _ in ()).throw(ValueError(value)))
    except (ValueError, UnicodeError):
        raise SmokeFailure("Private replay harness settings are invalid JSON") from None
    require(isinstance(settings, dict), "Replay harness settings must be an object")
    require(settings.get("harness_version") == HARNESS_VERSION,
            "Unsupported replay harness settings version")
    require(settings.get("kind") == SETTINGS_KIND,
            "Replay harness settings kind is invalid")
    require(settings.get("synthetic") is True and settings.get("production_writes") is False,
            "Replay harness must be synthetic with production writes disabled")
    require(settings.get("environment") == "development",
            "Replay harness environment must be development")
    api_url = loopback(settings.get("api_url"))
    fixture_url = loopback(settings.get("fixture_url"))
    namespace = dedicated_namespace(settings.get("namespace"))
    require(settings.get("fixture_kind") == FIXTURE_KIND,
            "Replay harness requires the full-stack/newSSE fixture")
    require(settings.get("new_sse_mode") == NEW_SSE_MODE,
            "Ordinary newSSE development mode is forbidden; use replay_capacity_fixture")
    report_dir = _private_directory(settings.get("report_dir"))
    timeout = settings.get("hook_timeout_seconds")
    require(type(timeout) is int and 1 <= timeout <= 21_600,
            "hook_timeout_seconds must be between 1 and 21600")
    commands = settings.get("commands")
    require(isinstance(commands, dict) and set(commands) == set(HOOK_NAMES),
            "commands must provide every fixed replay fixture hook")
    commands = {name: _command(commands[name], name) for name in HOOK_NAMES}
    resources = _resources(settings.get("resources"))
    fingerprint = hashlib.sha256(raw).hexdigest()
    return {
        "path": path,
        "fingerprint": fingerprint,
        "api_url": api_url,
        "fixture_url": fixture_url,
        "namespace": namespace,
        "report_dir": report_dir,
        "hook_timeout_seconds": timeout,
        "commands": commands,
        "resources": resources,
    }


def cell_id(profile, budget):
    return f"{profile}-{budget}"


def cell_report_path(settings, profile, budget):
    return settings["report_dir"] / (cell_id(profile, budget) + ".json")


def fixture_values(namespace, profile, budget):
    """Return deterministic synthetic seed and canaries without persisting plaintext."""
    seed = hashlib.sha256(
        f"evia-replay-capacity-v1/{namespace}/{profile}/{budget}".encode("utf-8")
    ).hexdigest()
    return {
        "seed": seed,
        "password_canary": "evia-synthetic-password-" + seed[:20],
        "masked_canary": "evia-synthetic-masked-" + seed[20:40],
    }


def _hook_environment(settings, profile, budget, hook):
    fixture = fixture_values(settings["namespace"], profile, budget)
    return {
        "PATH": "/usr/local/bin:/usr/bin:/bin",
        "HOME": "/tmp",
        "TMPDIR": str(settings["report_dir"]),
        "LC_ALL": "C.UTF-8",
        "NO_PROXY": "127.0.0.1,::1",
        "no_proxy": "127.0.0.1,::1",
        "EVIA_REPLAY_SETTINGS": str(settings["path"]),
        "EVIA_REPLAY_HOOK": hook,
        "EVIA_REPLAY_API_URL": settings["api_url"],
        "EVIA_REPLAY_FIXTURE_URL": settings["fixture_url"],
        "EVIA_REPLAY_NAMESPACE": settings["namespace"],
        "EVIA_REPLAY_CELL_ID": cell_id(profile, budget),
        "EVIA_REPLAY_PROFILE": profile,
        "EVIA_REPLAY_BUDGET_ENCODED_CHARS": str(budget),
        "EVIA_REPLAY_SEED": fixture["seed"],
        "EVIA_REPLAY_PASSWORD_CANARY": fixture["password_canary"],
        "EVIA_REPLAY_MASKED_CANARY": fixture["masked_canary"],
        "EVIA_REPLAY_NETWORK_JSON": json.dumps(
            NETWORK_PROFILES[profile], sort_keys=True, separators=(",", ":")
        ),
        "EVIA_REPLAY_RESOURCES_JSON": json.dumps(
            settings["resources"][profile], sort_keys=True, separators=(",", ":")
        ),
        "EVIA_REPLAY_SYNTHETIC": "1",
        "EVIA_REPLAY_PRODUCTION_WRITES": "0",
        "EVIA_REPLAY_ENVIRONMENT": "development",
        "EVIA_REPLAY_NEW_SSE_MODE": NEW_SSE_MODE,
    }


def run_hook(settings, profile, budget, hook):
    """Execute one no-shell hook and parse its bounded JSON object response."""
    command = settings["commands"][hook]
    environment = _hook_environment(settings, profile, budget, hook)
    try:
        with tempfile.TemporaryFile(dir=settings["report_dir"]) as stdout, \
                tempfile.TemporaryFile(dir=settings["report_dir"]) as stderr:
            completed = subprocess.run(
                command,
                stdin=subprocess.DEVNULL,
                stdout=stdout,
                stderr=stderr,
                cwd="/tmp",
                env=environment,
                timeout=settings["hook_timeout_seconds"],
                check=False,
            )
            stdout_size = stdout.tell()
            stderr_size = stderr.tell()
            require(stdout_size <= MAX_HOOK_OUTPUT_BYTES and stderr_size <= MAX_HOOK_OUTPUT_BYTES,
                    f"{hook} hook output exceeded its byte bound")
            require(completed.returncode == 0, f"{hook} hook failed")
            stdout.seek(0)
            encoded = stdout.read()
    except subprocess.TimeoutExpired:
        raise SmokeFailure(f"{hook} hook timed out") from None
    try:
        result = json.loads(
            encoded.decode("utf-8"),
            parse_constant=lambda value: (_ for _ in ()).throw(ValueError(value)),
        )
    except (ValueError, UnicodeError):
        raise SmokeFailure(f"{hook} hook did not return valid JSON") from None
    require(isinstance(result, dict), f"{hook} hook must return a JSON object")
    return result


def _prepare_ack(value, settings, profile, budget):
    expected = {
        "fixture_kind": FIXTURE_KIND,
        "new_sse_mode": NEW_SSE_MODE,
        "api_url": settings["api_url"],
        "fixture_url": settings["fixture_url"],
        "namespace": settings["namespace"],
        "cell_id": cell_id(profile, budget),
        "environment": "development",
        "synthetic": True,
        "production_writes": False,
        "dedicated_namespace": True,
        "ordinary_development_mode": False,
        "fresh_project": True,
    }
    require(all(value.get(key) == expected_value for key, expected_value in expected.items()),
            "prepare_cell did not attest the dedicated synthetic replay fixture boundary")
    project = value.get("project_fixture_id")
    require(isinstance(project, str) and 1 <= len(project) <= 128
            and all(character.isalnum() or character in "-_" for character in project),
            "prepare_cell must return a bounded synthetic project_fixture_id")


def _merge(target, addition, path="scenario"):
    require(isinstance(addition, dict), path + " hook section must be an object")
    for key, value in addition.items():
        child = path + "." + key
        if key not in target:
            target[key] = value
        elif isinstance(target[key], dict) and isinstance(value, dict):
            _merge(target[key], value, child)
        else:
            raise SmokeFailure(child + " was emitted by more than one hook")


def _sections(value, required, hook):
    require(isinstance(value, dict) and set(value) == set(required),
            f"{hook} must return exactly: " + ", ".join(required))
    require(all(isinstance(value[name], dict) for name in required),
            f"{hook} sections must be JSON objects")
    return value


def execute_cell(settings, profile, budget, hook_runner=run_hook):
    """Execute one fixture cell in the fixed safe stage order."""
    prepare = hook_runner(settings, profile, budget, "prepare_cell")
    _prepare_ack(prepare, settings, profile, budget)

    hold = hook_runner(settings, profile, budget, "worker_hold")
    require(hold == {"hold_armed": True}, "worker_hold must arm the dedicated fixture hold")

    initial = hook_runner(settings, profile, budget, "browser_workload")
    require(isinstance(initial, dict)
            and set(initial) == {"accepted_items_before_freeze", "accepted_chunks_before_transition"}
            and type(initial["accepted_items_before_freeze"]) is int
            and initial["accepted_items_before_freeze"] > 0
            and type(initial["accepted_chunks_before_transition"]) is int
            and initial["accepted_chunks_before_transition"] > 0,
            "browser_workload must report positive pre-freeze accepted item and chunk counts")

    transition_hook = "worker_drain" if profile == "baseline" else "worker_restart"
    transition = _sections(
        hook_runner(settings, profile, budget, transition_hook), ("worker",), transition_hook
    )
    pending_key = "pending_before_drain" if profile == "baseline" else "pending_before_restart"
    require(type(transition["worker"].get(pending_key)) is int
            and transition["worker"].get(pending_key) > 0,
            f"{transition_hook} must observe positive pending chunks")

    pagination = _sections(
        hook_runner(settings, profile, budget, "frozen_pagination"),
        ("browser", "pagination", "latency", "privacy"),
        "frozen_pagination",
    )
    require(type(pagination["browser"].get("accepted_items")) is int
            and pagination["browser"].get("accepted_items") >= initial["accepted_items_before_freeze"]
            and type(pagination["browser"].get("accepted_chunks")) is int
            and pagination["browser"].get("accepted_chunks") >= initial["accepted_chunks_before_transition"],
            "frozen_pagination cumulative browser counts regressed")

    reconciliation = _sections(
        hook_runner(settings, profile, budget, "reconcile"),
        ("clickhouse", "postgres", "latency", "privacy", "rss"),
        "reconcile",
    )

    scenario = {
        "name": cell_id(profile, budget),
        "profile": profile,
        "budget_encoded_chars": budget,
        "network": dict(NETWORK_PROFILES[profile]),
        "resources": dict(settings["resources"][profile]),
    }
    _merge(scenario, transition)
    _merge(scenario, pagination)
    _merge(scenario, reconciliation)
    return scenario


def _write_private_json(path, value):
    path = private_path(path)
    require(not path.exists(), "Replay harness refuses to replace an existing output file")
    encoded = json.dumps(value, indent=2, sort_keys=True, allow_nan=False) + "\n"
    require(len(encoded.encode("utf-8")) <= MAX_HOOK_OUTPUT_BYTES,
            "Replay harness JSON output exceeded the evaluator byte bound")
    flags = os.O_CREAT | os.O_EXCL | os.O_WRONLY
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    descriptor = os.open(path, flags, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
        stream.write(encoded)
    return path


def _cell_wrapper(settings, profile, budget, scenario):
    return {
        "cell_report_version": 1,
        "kind": CELL_REPORT_KIND,
        "settings_sha256": settings["fingerprint"],
        "api_url": settings["api_url"],
        "namespace": settings["namespace"],
        "cell_id": cell_id(profile, budget),
        "scenario": scenario,
    }


def load_cell_report(path, settings, profile, budget):
    path = private_path(path, existing=True)
    try:
        value = json.loads(
            path.read_text(encoding="utf-8"),
            parse_constant=lambda item: (_ for _ in ()).throw(ValueError(item)),
        )
    except (ValueError, UnicodeError):
        raise SmokeFailure("Existing replay cell report is invalid JSON") from None
    expected = {
        "cell_report_version": 1,
        "kind": CELL_REPORT_KIND,
        "settings_sha256": settings["fingerprint"],
        "api_url": settings["api_url"],
        "namespace": settings["namespace"],
        "cell_id": cell_id(profile, budget),
    }
    require(isinstance(value, dict)
            and all(value.get(key) == item for key, item in expected.items()),
            "Existing replay cell report does not match these private settings")
    scenario = value.get("scenario")
    require(isinstance(scenario, dict)
            and scenario.get("name") == cell_id(profile, budget)
            and scenario.get("profile") == profile
            and scenario.get("budget_encoded_chars") == budget
            and scenario.get("network") == NETWORK_PROFILES[profile]
            and scenario.get("resources") == settings["resources"][profile],
            "Existing replay cell scenario does not match its matrix cell")
    # Preserve measured gate failures as resumable evidence, but require the
    # scenario to remain parseable by the same pure evaluator used at assembly.
    reconcile_scenario(scenario)
    return scenario


def _existing_cells(settings):
    """Validate every pre-existing checkpoint before any fixture hook can run."""
    expected = {
        cell_report_path(settings, profile, budget).name
        for profile, budget in EXPECTED_SCENARIOS
    }
    unexpected = [entry.name for entry in settings["report_dir"].iterdir()
                  if entry.name not in expected]
    require(not unexpected,
            "Replay cell report directory contains an unexpected existing output file")
    existing = {}
    for profile, budget in EXPECTED_SCENARIOS:
        path = cell_report_path(settings, profile, budget)
        if os.path.lexists(path):
            existing[(profile, budget)] = load_cell_report(
                path, settings, profile, budget
            )
    return existing


def plan(settings):
    """Return a deterministic dry-run plan after all settings/commands validate."""
    existing = _existing_cells(settings)
    cells = []
    for profile, budget in EXPECTED_SCENARIOS:
        state = "resume" if (profile, budget) in existing else "run"
        cells.append({
            "cell_id": cell_id(profile, budget),
            "profile": profile,
            "budget_encoded_chars": budget,
            "network": dict(NETWORK_PROFILES[profile]),
            "resources": dict(settings["resources"][profile]),
            "state": state,
            "hooks": [
                "prepare_cell", "worker_hold", "browser_workload",
                "worker_drain" if profile == "baseline" else "worker_restart",
                "frozen_pagination", "reconcile",
            ] if state == "run" else [],
        })
    return {"stage": "dry_run", "validated": True, "commands_executed": False, "cells": cells}


def _summary_path(settings, value):
    path = private_path(value)
    require(not path.exists(), "Use a fresh replay capacity summary output path")
    require(path.parent.resolve() != settings["report_dir"].resolve(),
            "Keep the final summary outside the resumable cell report directory")
    return path


def run(settings, output, hook_runner=run_hook):
    """Run missing cells, resume completed cells, and write one evaluator input."""
    output = _summary_path(settings, output)
    existing = _existing_cells(settings)
    scenarios = []
    resumed = 0
    for profile, budget in EXPECTED_SCENARIOS:
        path = cell_report_path(settings, profile, budget)
        if (profile, budget) in existing:
            scenario = existing[(profile, budget)]
            resumed += 1
        else:
            scenario = execute_cell(settings, profile, budget, hook_runner=hook_runner)
            _write_private_json(path, _cell_wrapper(settings, profile, budget, scenario))
        scenarios.append(scenario)
    report = {
        "report_version": REPORT_VERSION,
        "kind": REPORT_KIND,
        "synthetic": True,
        "production_writes": False,
        "environment": "development",
        "api_url": settings["api_url"],
        "namespace": settings["namespace"],
        "scenarios": scenarios,
    }
    evaluation = evaluate_report(report)
    _write_private_json(output, report)
    return evaluation, resumed


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--settings", required=True,
                        help="Private mode-0600 harness settings under /tmp")
    parser.add_argument("--out", required=True,
                        help="Fresh mode-0600 evaluator-input report path under /tmp")
    parser.add_argument("--dry-run", action="store_true",
                        help="Validate settings, commands, output, and resume state without executing hooks")
    args = parser.parse_args(argv)

    # Validate the fresh final output before any hook can execute.
    output = private_path(args.out)
    require(not output.exists(), "Use a fresh replay capacity summary output path")
    settings = load_settings(args.settings)
    output = _summary_path(settings, output)
    if args.dry_run:
        print(json.dumps(plan(settings), sort_keys=True))
        return 0
    evaluation, resumed = run(settings, output)
    print(json.dumps({
        "stage": "complete",
        "passed": evaluation["passed"],
        "resumed_cells": resumed,
        "executed_cells": len(EXPECTED_SCENARIOS) - resumed,
        "private_output_written": True,
    }, sort_keys=True))
    return 0 if evaluation["passed"] else 1


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (SmokeFailure, OSError, ValueError, KeyError, TypeError) as error:
        reason = str(error) if isinstance(error, SmokeFailure) else "local validation failure"
        print(json.dumps({"stage": "failed", "reason": reason}), file=sys.stderr)
        raise SystemExit(2)
