"""Drain verification and bounded cold/warm query workload scheduling."""
from concurrent.futures import ThreadPoolExecutor
import json
from pathlib import Path
import time

from benchmark_common import require, SmokeFailure, timestamp, parse_time, distribution
from benchmark_workloads import workloads, request_base, validate_result

def run_queries(bench, config, args):
    bench.project = config["project_id"]
    cases = workloads(config)
    report = {"report_version": 1, "stage": "query", "started_at": timestamp(),
              "project_id": bench.project, "run_id": config["run_id"],
              "events_expected": args.events, "exact_uid_expected": min(args.events, config["uid_cardinality"]),
              "fixture": {"uid_cardinality": config["uid_cardinality"], "section_cardinality": 10,
                          "post_cardinality": 10000, "page_cardinality": 1000, "lookback_seconds": 1800},
              "query_time_range": request_base(config)["time_range"], "query_groups": [],
              "notes": ["Collection 202 means queue acknowledgement, not persisted facts.",
                        "First-observed samples do not imply an empty database or OS cache.",
                        "Warm samples repeat the same query; result metadata currently reports cache_hit=false.",
                        "Query latency includes submit, bounded polling, and result retrieval.",
                        "Latency percentiles cover successful samples; failed samples are counted separately by code.",
                        "Funnel is two ordered visits for one known UID; it measures a bounded identity slice."]}
    bench.report = report
    if args.ingestion_report:
        require(Path(args.ingestion_report).stat().st_size <= 1024 * 1024, "Ingestion report exceeded byte bound")
        ingestion = json.loads(Path(args.ingestion_report).read_text(encoding="utf-8"))
        require(ingestion.get("run_id") == config["run_id"] and ingestion.get("requested_unique_events") == args.events,
                "Ingestion report does not match fixture run and event count")
        report["ingestion_report"] = ingestion
        require(ingestion.get("complete") is True, "Ingestion was incomplete; inspect acknowledgement/drop evidence before draining")
    started = time.monotonic()
    deadline = started + args.drain_timeout
    observations = []
    report["visibility"] = {"drain_started_at": timestamp(), "count_observations": observations,
                            "exact_count_verified": False, "exact_uid_verified": False}
    while True:
        measured = bench.query(cases["count"])
        rows = measured["result"]["rows"]
        require(len(rows) == 1 and isinstance(rows[0].get("metric_0"), (int, float)), "Drain count result is invalid")
        count = rows[0]["metric_0"]
        require(count <= args.events, "Visible count exceeds unique fixture expectation")
        observations.append({"elapsed_ms": round((time.monotonic()-started)*1000, 3), "visible_count": count})
        if count == args.events:
            validate_result("count", measured["result"], config, args.events)
            validate_result("exact_uid", bench.query(cases["exact_uid"])["result"], config, args.events)
            break
        require(time.monotonic() < deadline, "Drain timed out before exact count and UID verification")
        time.sleep(min(1.0, max(.05, deadline-time.monotonic())))
    report["visibility"].update(verified_at=timestamp(), drain_ms=round((time.monotonic()-started)*1000, 3),
                                exact_count_verified=True, exact_uid_verified=True)
    if args.ingestion_report:
        for source, target in (("started_at", "ingestion_start_to_verified_ms"), ("finished_at", "ingestion_end_to_verified_ms")):
            if source in ingestion:
                report["visibility"][target] = round((parse_time(report["visibility"]["verified_at"]) - parse_time(ingestion[source])).total_seconds()*1000, 3)
    print(json.dumps({"stage": "visibility_verified", "events": args.events,
                      "exact_uid": report["exact_uid_expected"], "drain_ms": report["visibility"]["drain_ms"]}), flush=True)
    if args.action == "drain":
        report.update(stage="drain", success=True, query_jobs=bench.query_jobs, http_status_counts=dict(bench.http.statuses))
        return report
    for name, request in cases.items():
        try:
            first = bench.query(request)
            rows = validate_result(name, first["result"], config, args.events)
            first_group = {"successes": 1, "failures": [], "rows": [rows], "latency": distribution([first["latency_ms"]]),
                           "submit_latency": distribution([first["submit_ms"]])}
        except SmokeFailure as error:
            first_group = {"successes": 0, "failures": [str(error)], "rows": [], "latency": distribution([]), "submit_latency": distribution([])}
        report["query_groups"].append({"workload": name, "phase": "first_observed", "concurrency": 1, **first_group})
        for concurrency in args.concurrency:
            def sample(_):
                try:
                    measured = bench.query(request)
                    row_count = validate_result(name, measured["result"], config, args.events)
                    return {"latency_ms": measured["latency_ms"], "submit_ms": measured["submit_ms"], "rows": row_count}
                except SmokeFailure as error:
                    return {"failure": str(error)}
            with ThreadPoolExecutor(max_workers=concurrency) as executor:
                samples = list(executor.map(sample, range(args.samples)))
            good = [sample for sample in samples if "failure" not in sample]
            report["query_groups"].append({"workload": name, "phase": "warm", "concurrency": concurrency,
                "successes": len(good), "failures": [sample["failure"] for sample in samples if "failure" in sample],
                "rows": sorted({sample["rows"] for sample in good}),
                "latency": distribution([sample["latency_ms"] for sample in good]),
                "submit_latency": distribution([sample["submit_ms"] for sample in good])})
            print(json.dumps({"stage": "query_group", "workload": name, "concurrency": concurrency,
                              "successes": len(good), "failures": len(samples)-len(good)}), flush=True)
    report["cancellation"] = bench.query(cases["count"], cancel=True)
    report["http_status_counts"] = dict(bench.http.statuses)
    report["query_jobs"] = bench.query_jobs
    report["success"] = all(not group["failures"] for group in report["query_groups"])
    return report


