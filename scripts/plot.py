#!/usr/bin/env python3
"""
plot.py — generate latency and RIF plots from experiment CSV output.

Usage:
    python scripts/plot.py --input results.csv --experiment b --output plots/
    python scripts/plot.py --input results.csv --experiment a --output plots/
"""
import argparse
import os
import sys

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
import pandas as pd


ALGO_STYLE = {
    "prequal":    {"color": "#1f77b4", "marker": "o", "label": "Prequal (HCL)"},
    "roundrobin": {"color": "#ff7f0e", "marker": "s", "label": "Round-Robin"},
}


COLUMNS = [
    "experiment", "algorithm", "step_label",
    "probe_pool_size", "probe_rate_multiplier",
    "p99_ms", "p999_ms",
    "rif_p50", "rif_p75", "rif_p99",
    "total_requests", "errors", "timestamp",
]


def load(path: str, experiment: str) -> pd.DataFrame:
    # Detect whether the file starts with a header row.
    with open(path) as fh:
        first = fh.readline().split(",")[0].strip()
    has_header = first.lower() == "experiment"
    df = pd.read_csv(path, names=COLUMNS, header=0 if has_header else None)
    # Drop rows where the experiment column looks like a header (repeated appends).
    df = df[df["experiment"].str.upper() == experiment.upper()].copy()
    # Coerce numeric columns.
    for col in ["p99_ms", "p999_ms", "rif_p50", "rif_p75", "rif_p99",
                "probe_pool_size", "probe_rate_multiplier"]:
        df[col] = pd.to_numeric(df[col], errors="coerce")
    if df.empty:
        sys.exit(f"No rows for experiment '{experiment}' in {path}")
    return df


def x_axis(df: pd.DataFrame, experiment: str):
    if experiment.upper() == "A":
        col = "probe_rate_multiplier"
        label = "Probe rate / query rate"
        # sort numerically
        xs = sorted(df[col].unique())
        tick_labels = [f"{v:.2f}" for v in xs]
    else:
        col = "probe_pool_size"
        label = "Probe pool size P"
        xs = sorted(df[col].unique())
        tick_labels = [str(int(v)) for v in xs]
    return col, xs, label, tick_labels


def plot_latency(df: pd.DataFrame, experiment: str, out_dir: str):
    col, xs, xlabel, tick_labels = x_axis(df, experiment)

    fig, ax = plt.subplots(figsize=(7, 4))

    for algo, style in ALGO_STYLE.items():
        sub = df[df["algorithm"] == algo].groupby(col)[["p99_ms", "p999_ms"]].mean().reindex(xs)
        ax.plot(range(len(xs)), sub["p99_ms"],
                color=style["color"], marker=style["marker"],
                label=f"{style['label']} p99", linewidth=1.8)
        ax.plot(range(len(xs)), sub["p999_ms"],
                color=style["color"], marker=style["marker"],
                linestyle="--", alpha=0.6,
                label=f"{style['label']} p99.9", linewidth=1.2)

    ax.set_xticks(range(len(xs)))
    ax.set_xticklabels(tick_labels)
    ax.set_xlabel(xlabel)
    ax.set_ylabel("Latency (ms)")
    ax.set_title(f"Experiment {experiment.upper()} — Latency Tail")
    ax.legend(fontsize=8)
    ax.grid(True, alpha=0.3)
    fig.tight_layout()

    path = os.path.join(out_dir, f"exp{experiment.upper()}_latency.png")
    fig.savefig(path, dpi=150)
    print(f"saved {path}")
    plt.close(fig)


def plot_rif(df: pd.DataFrame, experiment: str, out_dir: str):
    col, xs, xlabel, tick_labels = x_axis(df, experiment)

    fig, ax = plt.subplots(figsize=(7, 4))

    for algo, style in ALGO_STYLE.items():
        sub = df[df["algorithm"] == algo].groupby(col)[["rif_p50", "rif_p75", "rif_p99"]].mean().reindex(xs)
        ax.plot(range(len(xs)), sub["rif_p99"],
                color=style["color"], marker=style["marker"],
                label=f"{style['label']} p99 RIF", linewidth=1.8)
        ax.fill_between(range(len(xs)), sub["rif_p50"], sub["rif_p75"],
                        color=style["color"], alpha=0.15,
                        label=f"{style['label']} p50–p75 band")

    ax.set_xticks(range(len(xs)))
    ax.set_xticklabels(tick_labels)
    ax.set_xlabel(xlabel)
    ax.set_ylabel("RIF at dispatch")
    ax.set_title(f"Experiment {experiment.upper()} — RIF Distribution")
    ax.legend(fontsize=8)
    ax.grid(True, alpha=0.3)
    fig.tight_layout()

    path = os.path.join(out_dir, f"exp{experiment.upper()}_rif.png")
    fig.savefig(path, dpi=150)
    print(f"saved {path}")
    plt.close(fig)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--input", default="results.csv")
    ap.add_argument("--experiment", default="b", help="a or b")
    ap.add_argument("--output", default="plots")
    args = ap.parse_args()

    os.makedirs(args.output, exist_ok=True)

    df = load(args.input, args.experiment)
    plot_latency(df, args.experiment, args.output)
    plot_rif(df, args.experiment, args.output)


if __name__ == "__main__":
    main()
