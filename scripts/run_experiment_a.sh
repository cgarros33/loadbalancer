#!/usr/bin/env bash
# Experiment A — vary probe rate under dynamic per-backend antagonist load
# (Figure 8 replica: probe-rate sweep at ~1.5x our CPU allocation, worst case).
#
# Each algorithm runs in its own isolated compose lifecycle so routing decisions
# of one do not affect backend load seen by the other (matching the paper's setup).
#
# Eje A (allocation vs load): capacity=10 is our guaranteed CPU allocation;
# QPS=500 -> 50 QPS/backend query load. With base-service-ms=60,
# C_throughput = capacity*1000/base_ms = 166.7 QPS/backend, so our fixed
# query load is 50/166.7 ~= 30% of C.
#
# Eje B (dynamic antagonist): each backend independently runs a 5-state random
# walk over antagonist load in {20,35,50,65,80}% of capacity (floor 20%,
# ceiling 80%, exponential dwell mean=2s). effectiveC = capacity*(1-antagonist/100):
#   antagonist=20% (floor) -> effectiveC=8 -> rho = 50/(8*1000/60)  = 0.375
#   antagonist=80% (max)   -> effectiveC=2 -> rho = 50/(2*1000/60)  = 1.5
# matching the paper's "1.5x our CPU allocation" worst case with NO QPS change.
#
# Backends are split into 3 hot-prone (bias 0.65), 4 neutral (bias 0.5), and
# 3 cold-prone (bias 0.3) groups, so the system-wide average antagonist load
# stays well under the no-collapse threshold while still exhibiting the
# heterogeneity Prequal is designed to exploit.
#
# Usage: ./scripts/run_experiment_a.sh [OUTPUT]
#   OUTPUT  CSV file path (default: results_a.csv)

set -euo pipefail

OUTPUT=${1:-results_a.csv}
BINARY=./bin/experiment

if [[ ! -x "$BINARY" ]]; then
    echo "binary not found — run: make build" >&2
    exit 1
fi

echo "=== Experiment A: probe rate sweep at 1.5× allocation ==="
echo "output: $OUTPUT"
echo ""

"$BINARY" \
    --experiment       a       \
    --qps              500     \
    --backends         10      \
    --capacity         10      \
    --base-service-ms  60      \
    --hot-bias         0.65    \
    --neutral-bias     0.5     \
    --cold-bias        0.3     \
    --mean-dwell-ms    2000    \
    --qrif             0.70    \
    --duration         30s     \
    --warmup           0s      \
    --drain            0s      \
    --output           "$OUTPUT"

echo ""
echo "done — results in $OUTPUT"
echo "plot: python3 scripts/plot.py --input $OUTPUT --experiment a --output plots/"
