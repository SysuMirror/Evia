#!/usr/bin/env python3
"""Read-only post-measurement ClickHouse query-log and index-plan evidence.

Uses private benchmark settings; never flushes logs/caches or executes fact queries.
First-observed samples are not cold-cache evidence; normalized groups mix initial
and warm runs. Query-log writes lag completion, so rerun later with the SAME end
bound if recent rows are missing. Empty logs do not establish absent executions:
project filtering requires the fixture UUID to appear literally in logged SQL.
EXPLAIN outlines describe planning/index selection, not measured execution costs.
"""
import argparse
import base64
from datetime import datetime, timedelta, timezone
import json
import math
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
import uuid

from benchmark_common import (NoRedirect, SmokeFailure, load_config, load_environment,
                              parse_time, private_path, require, timestamp)
from benchmark_workloads import uid

ENDPOINT = "http://127.0.0.1:18124"
MAX_RESPONSE = 1024 * 1024
GROUP_LIMIT = 256


def utc_time(value):
    require(isinstance(value, str) and (value.endswith("Z") or value.endswith("+00:00")),
            "UTC ISO timestamp required")
    return parse_time(value)


def clickhouse_time(value):
    return value.astimezone(timezone.utc).strftime("%Y-%m-%d %H:%M:%S.%f")


class Diagnostics:
    def __init__(self, runtime):
        _, settings = load_environment(private_path(runtime, existing=True))
        require(settings.get("EVIA_CLICKHOUSE_URL") in (ENDPOINT, ENDPOINT + "/"),
                "Dedicated benchmark endpoint required")
        self.database = settings.get("EVIA_CLICKHOUSE_DATABASE", "")
        require(re.fullmatch(r"evia_bench_[a-f0-9]{12}", self.database), "Benchmark database required")
        user, password = settings.get("EVIA_CLICKHOUSE_USERNAME"), settings.get("EVIA_CLICKHOUSE_PASSWORD")
        require(user and password, "Explicit ClickHouse credentials required")
        self.authorization = "Basic " + base64.b64encode((user + ":" + password).encode()).decode()
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())

    def read(self, sql, parameters):
        require(sql.startswith(("SELECT ", "EXPLAIN indexes = 1 SELECT ")) and ";" not in sql,
                "Only fixed read-only diagnostics SQL is allowed")
        options = {"database": self.database, "readonly": "2", "max_execution_time": "25",
                   "max_threads": "1", "max_memory_usage": "67108864", "max_rows_to_read": "2000000",
                   "max_result_bytes": str(MAX_RESPONSE), "result_overflow_mode": "throw",
                   "output_format_json_quote_64bit_integers": "0",
                   "query_id": "evia-diagnostics-" + str(uuid.uuid4())}
        options.update({"param_" + key: str(value) for key, value in parameters.items()})
        request = urllib.request.Request(ENDPOINT + "/?" + urllib.parse.urlencode(options),
                                        data=(sql + " FORMAT JSONEachRow").encode(), method="POST",
                                        headers={"Authorization": self.authorization,
                                                 "Content-Type": "text/plain; charset=utf-8"})
        try:
            with self.opener.open(request, timeout=30) as response:
                require(response.status == 200, "ClickHouse diagnostics request failed")
                body = response.read(MAX_RESPONSE + 1)
            require(len(body) <= MAX_RESPONSE, "ClickHouse diagnostics response exceeded byte bound")
            return [json.loads(line) for line in body.splitlines() if line.strip()]
        except (urllib.error.URLError, OSError, ValueError):
            raise SmokeFailure("ClickHouse diagnostics transport or response failed") from None


def log_summary(client, config, since, until):
    sql = ("SELECT toString(normalized_query_hash) AS normalized_query_hash, exception_code, count() AS n, "
           "quantileExact(0.5)(query_duration_ms) AS p50_duration_ms, "
           "quantileExact(0.95)(query_duration_ms) AS p95_duration_ms, "
           + ", ".join(f"{op}({field}) AS {op}_{field}" for field in
                       ("read_rows", "read_bytes", "memory_usage") for op in ("min", "max", "avg"))
           + ", max(ProfileEvents['SelectedMarks']) AS max_selected_marks, "
           "max(ProfileEvents['UserTimeMicroseconds'] + ProfileEvents['SystemTimeMicroseconds']) AS max_cpu_microseconds "
           "FROM system.query_log WHERE type IN ('QueryFinish','ExceptionBeforeStart','ExceptionWhileProcessing') "
           "AND is_initial_query AND startsWith(query_id,'evia-analysis-') "
           "AND event_date >= toDate(toDateTime64({since:String},6,'UTC'))-1 "
           "AND event_date <= toDate(toDateTime64({until:String},6,'UTC'))+1 "
           "AND query_start_time_microseconds >= toDateTime64({since:String},6,'UTC') "
           "AND query_start_time_microseconds < toDateTime64({until:String},6,'UTC') "
           "AND current_database={database:String} AND position(query,{project:String})>0 "
           f"GROUP BY normalized_query_hash,exception_code ORDER BY normalized_query_hash,exception_code LIMIT {GROUP_LIMIT}")
    rows = client.read(sql, {"since": clickhouse_time(since), "until": clickhouse_time(until),
                             "project": config["project_id"], "database": client.database})
    fields = {"normalized_query_hash", "exception_code", "n", "p50_duration_ms", "p95_duration_ms",
              "max_selected_marks", "max_cpu_microseconds"}
    fields.update(op + "_" + field for field in ("read_rows", "read_bytes", "memory_usage")
                  for op in ("min", "max", "avg"))
    require(len(rows) <= GROUP_LIMIT, "Unexpected diagnostics group count")
    for row in rows:
        require(isinstance(row, dict) and set(row) == fields
                and re.fullmatch(r"[0-9]{1,20}", str(row["normalized_query_hash"]))
                and all(type(value) in (int, float) and math.isfinite(value) and value >= 0
                        for key, value in row.items() if key != "normalized_query_hash"),
                "Unexpected diagnostics fields or values")
    return rows


def plan_outline(rows):
    """Retain only known operator names and numeric index counters, never SQL."""
    nodes, counters = [], []
    allowed = {"Expression", "Filter", "Aggregating", "ReadFromMergeTree", "MergingAggregated",
               "Sorting", "Limit", "Indexes", "MinMax", "Partition", "PrimaryKey", "Skip"}
    for row in rows:
        require(isinstance(row, dict) and isinstance(row.get("explain"), str), "Unexpected EXPLAIN response")
        for line in row["explain"].splitlines():
            node = re.match(r"\s*([A-Za-z]+)\b", line)
            if node and node[1] in allowed:
                nodes.append(node[1])
            count = re.fullmatch(r"\s*(Parts|Granules): (\d+)/(\d+)\s*", line)
            if count:
                counters.append({"kind": count[1].lower(), "selected": int(count[2]), "total": int(count[3])})
    return {"operators": nodes, "index_counters": counters}


def explain_queries(client, config):
    anchor = parse_time(config["started_at"])
    base = {"project": config["project_id"], "event": config["event_key"], "version": config["schema_version"],
            "from": clickhouse_time(anchor - timedelta(minutes=30)), "to": clickhouse_time(anchor)}
    scope = ("project_id={project:String} AND environment='development' "
             "AND event_key={event:String} AND schema_version={version:UInt64} "
             "AND event_time>=toDateTime64({from:String},9,'UTC') "
             "AND event_time<toDateTime64({to:String},9,'UTC')")
    statement = f"EXPLAIN indexes = 1 SELECT count() FROM `{client.database}`.event_facts WHERE " + scope
    cases = [("count_wide_30min", base, ""),
             ("uid_selective_30min", {**base, "uid": uid(config)}, " AND uid={uid:String}"),
             ("count_narrow_10sec", {**base, "from": clickhouse_time(anchor - timedelta(seconds=10))}, "")]
    return [{"case": name, "time_range": {"from": params["from"], "to": params["to"]},
             **plan_outline(client.read(statement + suffix, params))} for name, params, suffix in cases]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for flag in ("runtime", "config", "since", "out"):
        parser.add_argument("--" + flag, required=True)
    parser.add_argument("--until", help="Exclusive UTC query-start bound; defaults to invocation time")
    args = parser.parse_args()
    config, output = load_config(args.config), private_path(args.out)
    since, until = utc_time(args.since), utc_time(args.until) if args.until else datetime.now(timezone.utc)
    require(since < until and until - since <= timedelta(days=1), "Diagnostic window must be positive and at most 24 hours")
    require(type(config.get("schema_version")) is int and 1 <= config["schema_version"] <= 9007199254740991,
            "Fixture schema version required")
    require(not output.exists(), "Use a fresh diagnostics output path")
    require(1970 <= parse_time(config["started_at"]).year <= 2100, "Fixture time outside supported range")
    client = Diagnostics(args.runtime)
    summary = log_summary(client, config, since, until)
    report = {"report_version": 1, "project_id": config["project_id"], "database": client.database,
              "since": timestamp(since), "until": timestamp(until), "generated_at": timestamp(),
              "query_log_groups": summary, "group_limit": GROUP_LIMIT,
              "possibly_truncated": len(summary) == GROUP_LIMIT, "explain": explain_queries(client, config),
              "notes": ["Read-only; no log flush, cache reset, or fact SELECT was executed.",
                        "Window uses query start times; only completed/exception log rows currently visible are included.",
                        "Log flush latency can hide recent rows; repeat later with identical since/until bounds.",
                        "Literal project-UUID filtering can omit parameterized SQL when logs retain placeholders.",
                        "Normalized hashes combine initial and warm samples; initial does not mean cold cache.",
                        "EXPLAIN outlines are representative plans, not measured query costs or full SQL."]}
    encoded = json.dumps(report, indent=2, allow_nan=False) + "\n"
    with os.fdopen(os.open(output, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600), "w") as stream:
        stream.write(encoded)
    print("Read-only diagnostics written; private values and SQL omitted.")


if __name__ == "__main__":
    try:
        main()
    except (SmokeFailure, OSError, ValueError, KeyError, TypeError, SystemExit) as error:
        if isinstance(error, SystemExit) and error.code in (0, 2):
            raise
        raise SystemExit("Query diagnostics failed (" + type(error).__name__ + "); values omitted.") from None
