#!/usr/bin/env bash
# Experiment B — vary probe pool size at a fixed probe-rate multiplier
# (Figure 9 replica), under the same dynamic per-backend antagonist load
# model as Experiment A.
#
# Each algorithm runs in its own isolated compose lifecycle (matching the
# paper). Load model matches run_experiment_a.sh: capacity=10,
# base-service-ms=60 -> C_throughput = 166.7 QPS/backend. QPS is chosen so
# QPS/backends ~= 50 (rho_0 ~= 0.3 at the antagonist floor), the same ratio
# used for the 10-backend baseline (qps=500).
#
# Usage: ./scripts/run_experiment_b.sh [OUTPUT] [BACKENDS] [QPS]
#   OUTPUT    CSV file path (default: results_b.csv)
#   BACKENDS  number of backend containers (default: 10)
#   QPS       target request rate; default scales with BACKENDS to keep
#             QPS/backends ~= 50 (e.g. 50 backends -> 2500 qps)

set -euo pipefail

OUTPUT=${1:-results_b.csv}
BACKENDS=${2:-10}
QPS=${3:-$((BACKENDS * 50))}
BINARY=./bin/experiment

if [[ ! -x "$BINARY" ]]; then
    echo "binary not found — run: make build" >&2
    exit 1
fi

echo "=== Experiment B: probe pool size sweep ==="
echo "output: $OUTPUT  backends: $BACKENDS  qps: $QPS"
echo ""

"$BINARY" \
    --experiment       b       \
    --qps              "$QPS"  \
    --backends         "$BACKENDS" \
    --capacity         10      \
    --base-service-ms  60      \
    --hot-bias         0.50    \
    --neutral-bias     0.425   \
    --cold-bias        0.3     \
    --mean-dwell-ms    2000    \
    --qrif             0.70    \
    --duration         30s     \
    --warmup           15s     \
    --drain            5s      \
    --output           "$OUTPUT"

echo ""
echo "done — results in $OUTPUT"
echo "plot: python3 scripts/plot.py --input $OUTPUT --experiment b --output plots/"
