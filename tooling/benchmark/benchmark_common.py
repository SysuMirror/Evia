"""Private configuration, shared HTTP boundary, and reporting policies."""
from datetime import datetime, timezone
import http.cookiejar
import ipaddress
import json
import math
import os
from pathlib import Path
import stat
import sys
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "tooling" / "verification"))
from backend_smoke import HTTP as SmokeHTTP, NoRedirect, SmokeFailure, identifier, require, resource_id, timestamp
from browser_runtime import load as load_environment

EVENT_KEY = "benchmark_page_view"
CONTRACT_VERSION = 1

def loopback(value):
    endpoint = urllib.parse.urlsplit(value)
    try:
        address = ipaddress.ip_address(endpoint.hostname or "")
        address = getattr(address, "ipv4_mapped", None) or address
    except ValueError:
        address = None
    require(endpoint.scheme == "http" and address is not None and address.is_loopback and "%" not in (endpoint.hostname or "")
            and endpoint.path in ("", "/") and not endpoint.query and not endpoint.fragment
            and endpoint.username is None and endpoint.password is None
            and not any(character.isspace() for character in value),
            "Benchmark API must be a literal loopback HTTP origin")
    return value.rstrip("/")


class HTTP(SmokeHTTP):
    """Reuse the smoke boundary with direct literal-loopback transport only."""
    def __init__(self, base, origin=None, session=False):
        super().__init__(loopback(base), origin, session=False)
        handlers = [urllib.request.ProxyHandler({}), NoRedirect()]
        if session:
            handlers.append(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        self.opener = urllib.request.build_opener(*handlers)


def private_path(value, existing=False):
    path = Path(value).expanduser().absolute()
    require(path.parent.resolve().is_relative_to(Path("/tmp")), "Private benchmark files must live under /tmp")
    require(not path.is_symlink(), "Private benchmark file must not be a symlink")
    if existing:
        mode = path.stat()
        require(stat.S_ISREG(mode.st_mode) and stat.S_IMODE(mode.st_mode) == 0o600,
                "Private benchmark file must be a regular mode 0600 file")
        require(mode.st_size <= 64 * 1024, "Private benchmark config exceeded its byte bound")
    return path


def load_config(value):
    path = private_path(value, existing=True)
    try:
        config = json.loads(path.read_text(encoding="utf-8"))
    except (ValueError, UnicodeError):
        raise SmokeFailure("Private benchmark config is invalid JSON") from None
    require(config.get("contract_version") == CONTRACT_VERSION, "Unsupported benchmark config version")
    loopback(config["api_url"])
    for key in ("project_id", "run_id"):
        resource_id(config.get(key))
    require(config.get("environment") == "development" and config.get("event_key") == EVENT_KEY,
            "Benchmark config must select its synthetic development fixture")
    require(isinstance(config.get("uid_cardinality"), int) and 1 <= config["uid_cardinality"] <= 1_000_000,
            "Invalid UID cardinality")
    parse_time(config["started_at"])
    return config


def credentials():
    settings = dict(os.environ)
    if settings.get("EVIA_BENCH_CREDENTIALS"):
        path = private_path(settings["EVIA_BENCH_CREDENTIALS"], existing=True)
        _, values = load_environment(path)
        settings.update({key: value for key, value in values.items()
                         if key in ("EVIA_E2E_USERNAME", "EVIA_E2E_PASSWORD", "EVIA_PUBLIC_ORIGIN", "EVIA_E2E_API_URL")})
    require(bool(settings.get("EVIA_E2E_USERNAME") and settings.get("EVIA_E2E_PASSWORD")),
            "Explicit EVIA_E2E_USERNAME and EVIA_E2E_PASSWORD are required")
    return settings


def parse_time(value):
    parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    require(parsed.tzinfo is not None, "Timestamp must carry a timezone")
    return parsed.astimezone(timezone.utc)


def delta_ns(value):
    return (value.days * 86400 + value.seconds) * 1_000_000_000 + value.microseconds * 1000


def distribution(samples):
    """Nearest-rank percentiles; preserve small-sample limits explicitly."""
    values = sorted(samples)
    if not values:
        return {"n": 0, "small_sample": True, "percentile_method": "nearest_rank"}
    def percentile(p):
        return round(values[max(0, math.ceil(p * len(values)) - 1)], 3)
    return {"n": len(values), "small_sample": len(values) < 30, "percentile_method": "nearest_rank",
            "min_ms": round(values[0], 3), "mean_ms": round(sum(values) / len(values), 3),
            "p50_ms": percentile(.50), "p95_ms": percentile(.95), "p99_ms": percentile(.99),
            "max_ms": round(values[-1], 3)}


def safe_code(job):
    import re
    code = job.get("error", {}).get("code", "NO_ERROR_CODE")
    return code if isinstance(code, str) and re.fullmatch(r"[A-Z][A-Z0-9_]{0,100}", code) else "UNKNOWN_ERROR"


