#!/usr/bin/env python3
"""Prepare synthetic fixtures, verify ingestion, and benchmark real API queries.

Use EVIA_BENCH_CONFIG for the private mode-0600 /tmp Go load-generator config.
Use explicit EVIA_E2E credentials or EVIA_BENCH_CREDENTIALS=/tmp/private.env.
"""
import argparse
import json
import os
from pathlib import Path
import sys

from benchmark_common import (SmokeFailure, credentials, private_path, load_config, require,
                              distribution, loopback, parse_time, timestamp, EVENT_KEY)
from benchmark_api import Benchmark
from benchmark_workloads import matching_modulo, validate_result, workloads, uid
from benchmark_queries import run_queries

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="action", required=True)
    prepare = sub.add_parser("prepare", help="Create a fresh public-API synthetic project and private Go config")
    prepare.add_argument("--api-url", default=os.environ.get("EVIA_E2E_API_URL", "http://127.0.0.1:8080"))
    prepare.add_argument("--uid-cardinality", type=int, default=100000)
    prepare.add_argument("--config", default=os.environ.get("EVIA_BENCH_CONFIG"), required=not os.environ.get("EVIA_BENCH_CONFIG"))
    query = sub.add_parser("queries", help="Verify ingestion visibility and measure actual async queries")
    query.add_argument("--config", default=os.environ.get("EVIA_BENCH_CONFIG"), required=not os.environ.get("EVIA_BENCH_CONFIG"))
    query.add_argument("--events", type=int, required=True)
    query.add_argument("--samples", type=int, default=30)
    query.add_argument("--concurrency", default="1,4,8")
    query.add_argument("--drain-timeout", type=int, default=600)
    query.add_argument("--report", required=True)
    query.add_argument("--ingestion-report")
    drain = sub.add_parser("drain", help="Verify exact visible count and UID cardinality only")
    drain.add_argument("--config", default=os.environ.get("EVIA_BENCH_CONFIG"), required=not os.environ.get("EVIA_BENCH_CONFIG"))
    drain.add_argument("--events", type=int, required=True)
    drain.add_argument("--drain-timeout", type=int, default=600)
    drain.add_argument("--report", required=True)
    drain.add_argument("--ingestion-report")
    drain.set_defaults(samples=1, concurrency="1")
    args = parser.parse_args()
    settings = credentials()
    bench = None
    try:
        if args.action == "prepare":
            require(1 <= args.uid_cardinality <= 1_000_000, "UID cardinality must be between 1 and 1000000")
            path = private_path(args.config)
            require(not path.exists(), "Private benchmark config already exists; use a fresh path")
            bench = Benchmark(args.api_url, settings)
            config = bench.prepare(args.uid_cardinality)
            descriptor = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
            with os.fdopen(descriptor, "w") as stream:
                json.dump(config, stream, indent=2)
                stream.write("\n")
            print(json.dumps({"stage": "prepared", "project_id": config["project_id"], "run_id": config["run_id"], "config_path": str(path)}))
        else:
            require(1 <= args.events <= 1_000_000, "Event count must be between 1 and 1000000")
            require(1 <= args.samples <= 1000 and 1 <= args.drain_timeout <= 1800, "Sample or drain budget invalid")
            args.concurrency = [int(value) for value in args.concurrency.split(",")]
            require(bool(args.concurrency) and all(value in (1, 4, 8) for value in args.concurrency)
                    and len(set(args.concurrency)) == len(args.concurrency), "Concurrency must select distinct values from 1,4,8")
            config = load_config(args.config)
            require(matching_modulo(args.events, config["uid_cardinality"]) <= 500, "Known UID journey exceeds snapshot budget; increase UID cardinality")
            bench = Benchmark(config["api_url"], settings)
            try:
                report = run_queries(bench, config, args)
            except SmokeFailure as error:
                report = getattr(bench, "report", {"report_version": 1, "stage": "query"})
                report.update(success=False, failure=str(error), query_jobs=bench.query_jobs,
                              http_status_counts=dict(bench.http.statuses))
                Path(args.report).write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
                raise
            Path(args.report).write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
            print(json.dumps({"stage": "queries_complete", "success": report["success"], "query_jobs": bench.query_jobs, "report": args.report}))
            return 0 if report["success"] else 1
    finally:
        if bench:
            bench.logout()
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (SmokeFailure, OSError, ValueError, KeyError) as error:
        message = str(error) if isinstance(error, SmokeFailure) else "Local benchmark failure: " + type(error).__name__
        print(json.dumps({"stage": "failed", "reason": message}), file=sys.stderr)
        raise SystemExit(1)
