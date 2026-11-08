#!/usr/bin/env python3
"""Render the checked-in benchmark evidence; requires optional matplotlib."""
import argparse
import json
from pathlib import Path

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--report", default="docs/verification/data/load-2026-11-08.json")
    parser.add_argument("--out", default="docs/assets/load-2026-11-08.svg")
    args = parser.parse_args()
    reports = json.loads(Path(args.report).read_text())["reports"]
    plt.rcParams.update({"font.family": "DejaVu Sans", "font.size": 10,
                         "svg.fonttype": "none", "axes.spines.top": False,
                         "axes.spines.right": False, "axes.edgecolor": "#c4cbd4"})
    fig, axes = plt.subplots(1, 2, figsize=(11.5, 4.5))
    blue, teal = "#4c6ef5", "#278b7b"
    rates = [reports[key]["unique_accepted_events_per_second"]
             for key in ("100k-fixed-ingestion", "million-ingestion")]
    bars = axes[0].bar(["100k events", "1M events"], rates, color=[blue, teal], width=.48)
    axes[0].bar_label(bars, labels=[f"{value:,.0f}" for value in rates], padding=5)
    axes[0].set(title="A  Receipt throughput", ylabel="Unique accepted events / second", ylim=(0, 10000))
    concurrency = [1, 4, 8]
    for offset, key, label, color in ((-.18, "100k-fixed-queries", "Baseline", blue),
                                      (.18, "100k-final-queries", "Narrow projection", teal)):
        groups = reports[key]["query_groups"]
        values = [next(group["latency"]["p95_ms"] for group in groups
                       if group["workload"] == "count" and group["phase"] == "warm"
                       and group["concurrency"] == size) for size in concurrency]
        bars = axes[1].bar([i + offset for i in range(3)], values, width=.34, label=label, color=color)
        axes[1].bar_label(bars, labels=[f"{value:,.0f}" for value in values], padding=4, fontsize=9)
    axes[1].set(title="B  Count query p95, 100k events", ylabel="End-to-end latency (ms)",
                xlabel="Concurrent query clients", xticks=range(3), xticklabels=concurrency, ylim=(0, 5600))
    axes[1].legend(frameon=False, loc="upper left")
    for ax in axes:
        ax.grid(axis="y", color="#e8ebef", linewidth=.7)
        ax.set_axisbelow(True)
        ax.tick_params(axis="both", length=0, pad=8)
    fig.suptitle("Evia, local benchmark observations", x=.07, ha="left", fontsize=15, weight="semibold")
    fig.text(.07, .075, "A: 8 clients, 50 events/batch, 10% byte-identical replay included in elapsed time.", fontsize=8.5, color="#5d6470")
    fig.text(.07, .035, "B: successful samples only, 30 attempts/group. Recovery and merge layout differ; no SQL-only speedup claim.", fontsize=8.5, color="#5d6470")
    fig.subplots_adjust(left=.09, right=.98, bottom=.22, top=.82, wspace=.31)
    fig.savefig(args.out, metadata={"Date": None, "Creator": "Evia benchmark tooling"}, facecolor="white")
    plt.close(fig)


if __name__ == "__main__":
    main()
