#!/usr/bin/env python3
"""Render query outcomes and successful-sample latency from measured evidence."""
import argparse
import json
from pathlib import Path

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt


LABELS = {
    "count": "Count", "exact_uid": "Exact UID", "hour_bucket": "Hourly",
    "section_group": "Property group", "high_cardinality_uid_filter": "UID filter",
    "property_filter": "Property filter", "journey": "Journey", "ordered_funnel": "Funnel",
}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--report", default="docs/verification/data/capacity-2026-11-09.json")
    parser.add_argument("--out", default="docs/assets/capacity-2026-11-09.svg")
    args = parser.parse_args()
    report = json.loads(Path(args.report).read_text())["reports"]["million-final-queries"]
    groups = report["query_groups"]
    successes, failures = [], []
    for name in LABELS:
        selected = [group for group in groups if group["workload"] == name]
        if not selected:
            raise ValueError("The figure requires every declared workload")
        successes.append(sum(group["successes"] for group in selected))
        failures.append(sum(len(group["failures"]) for group in selected))

    plt.rcParams.update({"font.family": "DejaVu Sans", "font.size": 10,
                         "svg.fonttype": "none", "svg.hashsalt": "evia-query-capacity",
                         "axes.spines.top": False,
                         "axes.spines.right": False, "axes.edgecolor": "#c4cbd4"})
    fig, axes = plt.subplots(1, 2, figsize=(12, 5), gridspec_kw={"width_ratios": [1.2, 1]})
    positions = list(range(len(LABELS)))
    axes[0].barh(positions, successes, color="#4263eb", label="Succeeded", height=.65)
    axes[0].barh(positions, failures, left=successes, color="#d87362", label="Failed", height=.65)
    totals = [success + failure for success, failure in zip(successes, failures)]
    for index, (success, total) in enumerate(zip(successes, totals)):
        axes[0].text(total + 2, index, f"{success}/{total}", va="center", fontsize=9)
    axes[0].set(title="A  All attempted query outcomes", xlabel="Query attempts",
                yticks=positions, yticklabels=list(LABELS.values()), xlim=(0, max(totals) * 1.25))
    axes[0].invert_yaxis()
    handles, labels = axes[0].get_legend_handles_labels()
    fig.legend(handles, labels, frameon=False, loc="lower left", bbox_to_anchor=(.13, .12), ncol=2, fontsize=9)
    concurrency = [1, 4, 8]
    for name, color, marker in (("count", "#4263eb", "o"), ("exact_uid", "#228b79", "s"),
                                ("journey", "#9565b5", "^"), ("ordered_funnel", "#bb7b30", "D")):
        values = []
        for size in concurrency:
            matches = [group for group in groups if group["workload"] == name
                       and group["phase"] == "warm" and group["concurrency"] == size]
            if len(matches) != 1 or matches[0]["latency"]["n"] == 0:
                raise ValueError("A selected latency series lacks successful samples")
            values.append(matches[0]["latency"]["p95_ms"] / 1000)
        axes[1].plot(concurrency, values, label=LABELS[name], color=color, marker=marker, linewidth=1.8)
    axes[1].set(title="B  Successful-sample p95", xlabel="Concurrent query clients",
                ylabel="End-to-end latency (seconds)", xticks=concurrency, ylim=(0, None))
    axes[1].legend(frameon=False, loc="upper left", fontsize=9)
    for axis in axes:
        axis.grid(axis="x" if axis is axes[0] else "y", color="#e8ebef", linewidth=.7)
        axis.set_axisbelow(True)
        axis.tick_params(axis="both", length=0, pad=7)
    fig.suptitle(f"Evia, {report['events_expected']:,} synthetic events", x=.08, ha="left", fontsize=15)
    fig.text(.08, .085, "A: first-observed + repeated attempts; failures remain in the denominator.", fontsize=8.5, color="#5d6470")
    fig.text(.08, .045, "B: includes queueing and polling, successful samples only. Default execution budgets remain unchanged.", fontsize=8.5, color="#5d6470")
    fig.subplots_adjust(left=.14, right=.98, bottom=.28, top=.82, wspace=.4)
    fig.savefig(args.out, metadata={"Date": None, "Creator": "Evia benchmark tooling"}, facecolor="white")
    plt.close(fig)


if __name__ == "__main__":
    main()
