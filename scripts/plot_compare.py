#!/usr/bin/env python3
"""Plot hot vs cold backend latency curves from calibrate_compare.csv."""

import argparse
import sys
import pandas as pd
import matplotlib.pyplot as plt

parser = argparse.ArgumentParser()
parser.add_argument("--input", default="calibrate_compare.csv")
parser.add_argument("--output", default="plots/calibrate_compare.png")
args = parser.parse_args()

df = pd.read_csv(args.input)
if df.empty:
    print("empty input", file=sys.stderr)
    sys.exit(1)

fig, axes = plt.subplots(1, 2, figsize=(13, 5))
fig.suptitle("Hot vs Cold backend — hyperbolic model", fontsize=13)

colors = {"hot": "#d62728", "cold": "#1f77b4"}
labels = {"hot": "hot  (CPU_LOAD=80)", "cold": "cold (CPU_LOAD=20)"}

for ax, metric, title in zip(
    axes,
    ["p50_ms", "p99_ms"],
    ["p50 latency (ms)", "p99 latency (ms)"],
):
    for backend, grp in df.groupby("backend"):
        grp = grp.sort_values("qps")
        ax.plot(
            grp["qps"],
            grp[metric],
            marker="o",
            markersize=4,
            color=colors.get(backend, "gray"),
            label=labels.get(backend, backend),
        )
    ax.set_xlabel("QPS")
    ax.set_ylabel(title)
    ax.set_title(title)
    ax.legend()
    ax.grid(True, alpha=0.3)
    ax.set_ylim(bottom=0)

plt.tight_layout()

import os
os.makedirs(os.path.dirname(args.output) or ".", exist_ok=True)
plt.savefig(args.output, dpi=150)
print(f"saved: {args.output}")
