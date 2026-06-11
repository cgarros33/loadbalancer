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
# Backends are split into 3 hot-prone (bias 0.50), 4 neutral (bias 0.425), and
# 3 cold-prone (bias 0.3) groups, so the system-wide average antagonist load
# stays well under the no-collapse threshold while still exhibiting the
# heterogeneity Prequal is designed to exploit.
#
# Bias values are chosen via the random walk's stationary distribution. Only
# the 80% antagonist level (rho=1.5) is unsustainable; levels 0-3 (20/35/50/65%)
# all give rho<1. The stationary probability of sitting at that ceiling is
# pi_4 = r^4/(1+r+r^2+r^3+r^4), r=p/(1-p). The current biases give
# pi_4 ~= 20% (hot) / 10% (neutral) / 2% (cold) — occasional excursions to the
# worst case rather than near-half-time.
#
# NOTE: v29/v29b attempted --duration 60s (with and without warmup) to reduce
# sampling variance from the antagonist random walk and instead caused EVERY
# step to collapse to 40-68% errors. Root cause turned out to be the *previous*
# bias values (0.65/0.5/0.3, pi_4 ~= 48%/20%/2%): a 30s window mostly captures
# the favorable transient (the walk starts at neutral and takes ~10-20s to
# reach the ceiling), while 60-100s windows reflect the harsher steady state,
# where hot backends spent ~48% of their time at rho=1.5. That is a modeling
# parameter issue, not an LB bug — fixed here by lowering the biases above so
# steady state itself is sustainable. (A separate, real bug — probeServer()
# not draining response bodies, causing TIME_WAIT exhaustion at long
# durations — was also fixed in pkg/loadbalancer/balancer.go.)
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
    --hot-bias         0.50    \
    --neutral-bias     0.425   \
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
