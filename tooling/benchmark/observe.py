#!/usr/bin/env python3
"""Read-only benchmark resource samples; no process arguments, env, or logs.

python3 tooling/benchmark/observe.py --out /tmp/resources.jsonl --duration 1800 \
    --interval 5 --pid 1234 --pid 1235

Docker values retain CLI units; process CPU counters are cumulative clock ticks.
Container stats are samples, and host load/memory include unrelated workloads.
"""
import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import time

NAME = re.compile(r"evia-benchmark-[a-z0-9][a-z0-9_.-]{0,100}\Z")
UNITS = re.compile(r"[0-9. kKMGTPEiB/%]+\Z")
STOP = False


def docker(arguments, timeout):
    environment = {key: value for key, value in os.environ.items() if not key.startswith("DOCKER_")}
    result = subprocess.run(["docker", "--host=unix:///var/run/docker.sock", *arguments],
                            env=environment, capture_output=True, text=True, timeout=timeout)
    if result.returncode:
        raise RuntimeError("docker command failed")
    return result.stdout


def containers(timeout):
    try:
        names = docker(["ps", "--filter", "name=^evia-benchmark-", "--format", "{{.Names}}"], timeout).splitlines()
        names = [name for name in names if NAME.fullmatch(name)]
        if len(names) > 32:
            return {"error": "container_count_exceeded_32"}
        if not names:
            return {"items": [], "status": "no_matching_containers"}
        capabilities = docker_capabilities(timeout)
        processes = container_processes(names, timeout)
        rows = []
        for line in docker(["stats", "--no-stream", "--format", "{{json .}}", *names], timeout).splitlines():
            value = json.loads(line)
            if not isinstance(value, dict) or value.get("Name") not in names:
                raise ValueError("unexpected container")
            row = {"name": value["Name"], "field_status": {},
                   "init_process": processes.get(value["Name"], {"error": "inspect_pid_unavailable"})}
            for field in ("CPUPerc", "MemUsage", "MemPerc", "PIDs", "NetIO", "BlockIO"):
                item = value.get(field)
                if not isinstance(item, str) or len(item) > 128 or not UNITS.fullmatch(item):
                    row[field] = None
                    row["field_status"][field] = "unavailable" if item in (None, "", "--", "N/A") else "invalid_format"
                else:
                    row[field] = item
                    shared = capabilities.get("cgroup_driver") == "none" and field not in ("NetIO",)
                    row["field_status"][field] = "shared_cgroup_not_container_scoped" if shared else "reported_by_docker"
            rows.append(row)
        return {"items": rows, "status": "sampled", "capabilities": capabilities,
                "process_scope": "init process only; excludes child processes"}
    except (OSError, subprocess.SubprocessError, RuntimeError, ValueError, KeyError) as error:
        return {"error": type(error).__name__}


def docker_capabilities(timeout):
    try:
        values = docker(["info", "--format", "{{json .CgroupDriver}}\n{{json .MemoryLimit}}\n{{json .CPUCfsQuota}}"], timeout).splitlines()
        driver, memory, cpu = [json.loads(value) for value in values]
        return {"cgroup_driver": driver if driver in ("none", "systemd", "cgroupfs") else "unknown",
                "memory_limit_supported": memory if isinstance(memory, bool) else None,
                "cpu_quota_supported": cpu if isinstance(cpu, bool) else None}
    except (OSError, subprocess.SubprocessError, RuntimeError, ValueError):
        return {"error": "capabilities_unavailable"}


def container_processes(names, timeout):
    try:
        values = docker(["inspect", "--format", "{{.Name}} {{.State.Pid}}", *names], timeout).splitlines()
        result = {}
        for value in values:
            name, pid = value.split()
            name = name.removeprefix("/")
            if name in names and pid.isdecimal() and int(pid) > 0:
                result[name] = process(int(pid))
        return result
    except (OSError, subprocess.SubprocessError, RuntimeError, ValueError):
        return {}


def host():
    try:
        disk = shutil.disk_usage("/workspace")
        memory = {}
        for line in Path("/proc/meminfo").read_text().splitlines():
            key, _, value = line.partition(":")
            if key in ("MemTotal", "MemAvailable"):
                memory[key + "_bytes"] = int(value.split()[0]) * 1024
        return {"disk_total_bytes": disk.total, "disk_free_bytes": disk.free,
                "memory": memory, "load_1_5_15": list(os.getloadavg())}
    except (OSError, ValueError) as error:
        return {"error": type(error).__name__}


def process(pid):
    try:
        # comm may contain spaces or ')'; never include it in the evidence.
        fields = Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()
        rss_pages = int(fields[21])
        return {"pid": pid, "cpu_ticks": int(fields[11]) + int(fields[12]),
                "clock_ticks_per_second": os.sysconf("SC_CLK_TCK"),
                "start_ticks": int(fields[19]), "threads": int(fields[17]),
                "rss_bytes": rss_pages * os.sysconf("SC_PAGE_SIZE") if rss_pages > 0 else None,
                "rss_status": "measured" if rss_pages > 0 else "zero_or_unavailable"}
    except (OSError, ValueError, IndexError) as error:
        return {"pid": pid, "rss_bytes": None, "rss_status": "unavailable", "error": type(error).__name__}


def stop(_number, _frame):
    global STOP
    STOP = True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", required=True)
    parser.add_argument("--duration", type=float, default=1800)
    parser.add_argument("--interval", type=float, default=5)
    parser.add_argument("--pid", type=int, action="append", default=[])
    args = parser.parse_args()
    if not 1 <= args.duration <= 1800 or not 1 <= args.interval <= 60 or len(args.pid) > 32 or any(pid < 1 for pid in args.pid):
        parser.error("duration 1..1800, interval 1..60, and at most 32 positive PIDs required")
    signal.signal(signal.SIGINT, stop)
    signal.signal(signal.SIGTERM, stop)
    started, samples = time.monotonic(), 0
    descriptor = os.open(args.out, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    with os.fdopen(descriptor, "w") as stream:
        while not STOP and time.monotonic() - started < args.duration:
            before = time.monotonic()
            timeout = max(0.05, min(4, (args.duration - (before - started)) / 4))
            record = {"type": "sample", "timestamp": datetime.now(timezone.utc).isoformat(),
                      "elapsed_seconds": before - started, "host": host(),
                      "containers": containers(timeout), "processes": [process(pid) for pid in sorted(set(args.pid))]}
            record["sampling_seconds"] = time.monotonic() - before
            stream.write(json.dumps(record, sort_keys=True, allow_nan=False) + "\n")
            stream.flush()
            samples += 1
            remaining = min(args.interval - (time.monotonic() - before), args.duration - (time.monotonic() - started))
            # Short waits make an interrupt stop sampling promptly.
            while not STOP and remaining > 0:
                time.sleep(min(remaining, 0.2))
                remaining = min(args.interval - (time.monotonic() - before), args.duration - (time.monotonic() - started))
        stream.write(json.dumps({"type": "finished", "samples": samples, "interrupted": STOP,
                                 "elapsed_seconds": time.monotonic() - started}) + "\n")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError) as error:
        raise SystemExit("Resource sampler failed (" + type(error).__name__ + "); values omitted.") from None
