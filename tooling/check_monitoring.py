"""Gate Prometheus rules against the metrics the Evia binaries actually expose.

The observability stack must never page on a typo: every metric referenced by
the rule files under deploy/monitoring has to be registered in non-test Go
sources (or be a Prometheus built-in / a recording rule defined in the same
file). The check is dependency-free beyond PyYAML and runs as part of make check.
"""
from pathlib import Path
import json
import re
import sys

import yaml

ROOT = Path(__file__).resolve().parents[1]
MONITORING_DIR = ROOT / "deploy" / "monitoring"
RULES_FILE = MONITORING_DIR / "prometheus-rules.yaml"
RULE_TEST_FILE = MONITORING_DIR / "prometheus-rules.test.yaml"
DASHBOARD_FILE = MONITORING_DIR / "grafana-dashboard.json"

INSTRUMENT_RE = re.compile(r"\.(?:Counter|Gauge|Histogram)\(\s*\"([a-zA-Z_:][a-zA-Z0-9_:]*)\"")
HISTOGRAM_RE = re.compile(r"\.Histogram\(\s*\"([a-zA-Z_:][a-zA-Z0-9_:]*)\"")
RUNTIME_NAME_RE = re.compile(r"\"((?:evia|go|process)_[a-z0-9_]+)\"")
IDENTIFIER_RE = re.compile(r"[a-zA-Z_:][a-zA-Z0-9_:]*")
RUNTIME_FILE = ROOT / "backend" / "internal" / "platform" / "observe" / "runtime.go"

# PromQL reserved words, aggregations, functions and modifiers. Anything left
# after subtracting these must be a real metric or a locally defined recording
# rule, so missing entries fail loudly instead of silently weakening the gate.
PROMQL_KEYWORDS = {
    # Aggregation operators and join modifiers.
    "sum", "avg", "count", "min", "max", "group", "stddev", "stdvar", "topk",
    "bottomk", "count_values", "limitk", "limit_ratio", "group_left",
    "group_right", "by", "without", "on", "ignoring", "offset", "bool",
    # Set and matching operators spelled as words.
    "and", "or", "unless",
    # Functions.
    "abs", "absent", "absent_over_time", "avg_over_time", "ceil", "changes",
    "clamp", "clamp_max", "clamp_min", "day_of_month", "day_of_week",
    "day_of_year", "days_in_month", "deg", "delta", "deriv", "exp", "floor",
    "histogram_avg", "histogram_count", "histogram_fraction",
    "histogram_quantile", "histogram_sum", "holt_winters", "hour", "idelta",
    "increase", "inf", "irate", "label_join", "label_replace", "last_over_time",
    "ln", "log10", "log2", "max_over_time", "min_over_time", "minute", "month",
    "nan", "pi", "predict_linear", "present_over_time",
    "quantile_over_time", "rad", "rate", "resets", "round", "scalar", "sgn",
    "sort", "sort_by_label", "sort_by_label_desc", "sort_desc", "sqrt",
    "stddev_over_time", "stdvar_over_time", "sum_over_time", "time",
    "timestamp", "vector", "year",
}

SEVERITIES = {"critical", "warning"}
RECORD_NAME_RE = re.compile(r"^evia:slo_[a-z0-9_]+(?::[a-z0-9_]+)+$")


def emitted_metrics():
    """Collect every metric name registered by production Go code."""
    names = set()
    histograms = set()
    for path in (ROOT / "backend").rglob("*.go"):
        if path.name.endswith("_test.go"):
            continue
        body = path.read_text(encoding="utf-8")
        for match in INSTRUMENT_RE.finditer(body):
            names.add(match.group(1))
        for match in HISTOGRAM_RE.finditer(body):
            histograms.add(match.group(1))
    for match in RUNTIME_NAME_RE.finditer(RUNTIME_FILE.read_text(encoding="utf-8")):
        names.add(match.group(1))
    # Prometheus itself supplies scrape health.
    names.add("up")
    # Histogram exposition appends the conventional suffixes.
    for histogram in histograms:
        names.update(
            histogram + suffix for suffix in ("_bucket", "_sum", "_count")
        )
    return names


def strip_balanced(text, opener, closer):
    """Remove balanced opener..closer spans, supporting nested pairs."""
    result = []
    depth = 0
    for char in text:
        if char == opener:
            depth += 1
            continue
        if char == closer:
            depth = max(0, depth - 1)
            continue
        if depth == 0:
            result.append(char)
    return "".join(result)


def referenced_metrics(expr):
    """Extract metric-like identifiers from a PromQL expression."""
    text = re.sub(r"#.*", "", expr)
    # Drop string literals so label values cannot look like metrics.
    text = re.sub(r'"(?:[^"\\]|\\.)*"', "", text)
    text = re.sub(r"'(?:[^'\\]|\\.)*'", "", text)
    text = re.sub(r"`[^`]*`", "", text)
    # Label matcher blocks and aggregation clauses only contain label names.
    text = strip_balanced(text, "{", "}")
    for clause in ("by", "without", "on", "ignoring", "group_left", "group_right"):
        text = re.sub(clause + r"\s*\([^)]*\)", " ", text)
    # Range vectors and durations: 5m, 1h, 30s, 250ms ... The lookbehind keeps
    # the 5m inside identifiers such as evia:...:rate5m intact.
    text = re.sub(r"(?<![a-zA-Z0-9_:])\d+(?:\.\d+)?(?:ms|s|m|h|d|w|y)\b", " ", text)
    found = set()
    for token in IDENTIFIER_RE.findall(text):
        if token in PROMQL_KEYWORDS:
            continue
        found.add(token)
    return found


def main():
    failures = []
    allowlist = emitted_metrics()
    document = yaml.safe_load(RULES_FILE.read_text(encoding="utf-8"))
    if not isinstance(document, dict) or not isinstance(document.get("groups"), list):
        failures.append("rules file must define a top-level groups list")
        _finish(failures)
        return

    groups = document["groups"]
    rule_names = set()
    alert_names = set()
    referenced = set()

    for group in groups:
        if not group.get("name") or not isinstance(group.get("rules"), list):
            failures.append(f"group {group!r} needs a name and rules list")
            continue
        for rule in group["rules"]:
            expr = rule.get("expr")
            if not isinstance(expr, str) or not expr.strip():
                failures.append(f"rule {rule!r} is missing an expr")
                continue
            referenced |= referenced_metrics(expr)
            if "alert" in rule:
                name = rule["alert"]
                alert_names.add(name)
                severity = (rule.get("labels") or {}).get("severity")
                if severity not in SEVERITIES:
                    failures.append(f"alert {name} needs a severity label in {sorted(SEVERITIES)}")
                annotations = rule.get("annotations") or {}
                for key in ("summary", "description"):
                    if not isinstance(annotations.get(key), str) or not annotations[key].strip():
                        failures.append(f"alert {name} needs a non-empty {key} annotation")
            elif "record" in rule:
                name = rule["record"]
                if not RECORD_NAME_RE.match(name):
                    failures.append(f"recording rule {name} must match evia:slo_<domain>:<metric> convention")
            else:
                failures.append(f"rule {rule!r} is neither alert nor record")
                continue
            if name in rule_names:
                failures.append(f"duplicate rule name {name}")
            rule_names.add(name)

    unknown = sorted(metric for metric in referenced if metric not in allowlist and metric not in rule_names)
    for metric in unknown:
        failures.append(f"expression references metric {metric!r} that no Evia process emits")

    if RULE_TEST_FILE.exists():
        failures += _check_rule_test(alert_names, allowlist)
    if DASHBOARD_FILE.exists():
        failures += _check_dashboard(allowlist, rule_names)

    _finish(failures)


def _check_rule_test(alert_names, allowlist):
    failures = []
    test_doc = yaml.safe_load(RULE_TEST_FILE.read_text(encoding="utf-8"))
    for entry in test_doc.get("rule_files", []):
        if not (RULE_TEST_FILE.parent / entry).exists():
            failures.append(f"rule test references missing rule file {entry}")
    for case in test_doc.get("tests", []):
        for series in case.get("input_series", []):
            metric = series["series"].split("{", 1)[0].strip()
            if metric not in allowlist:
                failures.append(f"rule test feeds unknown metric {metric!r}")
        for check in case.get("alert_rule_test", []):
            if check["alertname"] not in alert_names:
                failures.append(f"rule test expects unknown alert {check['alertname']}")
    return failures


def _walk_exprs(node):
    """Yield every PromQL expr embedded in a Grafana dashboard JSON tree."""
    if isinstance(node, dict):
        expr = node.get("expr")
        if isinstance(expr, str) and expr.strip():
            yield expr
        for value in node.values():
            yield from _walk_exprs(value)
    elif isinstance(node, list):
        for item in node:
            yield from _walk_exprs(item)


def _check_dashboard(allowlist, rule_names):
    failures = []
    try:
        dashboard = json.loads(DASHBOARD_FILE.read_text(encoding="utf-8"))
    except json.JSONDecodeError as error:
        return [f"dashboard {DASHBOARD_FILE.name} is not valid JSON: {error}"]
    if not dashboard.get("title") or not isinstance(dashboard.get("panels"), list):
        failures.append("dashboard needs a title and a panels list")
    panel_ids = set()
    for panel in dashboard.get("panels", []):
        panel_id = panel.get("id")
        if panel_id in panel_ids:
            failures.append(f"dashboard panel id {panel_id} is duplicated")
        panel_ids.add(panel_id)
        if not panel.get("title") or not panel.get("type"):
            failures.append(f"dashboard panel {panel!r} needs id, title and type")
    for expr in _walk_exprs(dashboard):
        for metric in sorted(referenced_metrics(expr) - allowlist - rule_names):
            failures.append(f"dashboard expression references metric {metric!r} that no Evia process emits")
    return failures


def _finish(failures):
    if failures:
        print("Monitoring rule verification failed:\n" + "\n".join(f"- {item}" for item in failures))
        sys.exit(1)
    print("Monitoring rules verified against emitted metrics.")


if __name__ == "__main__":
    main()
