#!/usr/bin/env bash
# Experiment A — vary probe rate at ~1.5× hot backend saturation (Figure 8 replica).
#
# Each algorithm runs in its own isolated compose lifecycle so routing decisions
# of one do not affect backend load seen by the other (matching the paper's setup).
#
# Parameters match the paper more closely than the earlier 80% antagonist setup:
#   antagonist=50%  → effectiveServiceMS_hot  = 100ms (2×, not 5×)
#   antagonist=10%  → effectiveServiceMS_cold =  56ms (1.1×)
#   QPS=1500        → 150 QPS per backend (RR); hot saturates at 100 QPS → ρ=1.5
#   cold saturation = 10×1000/56 ≈ 178 QPS → ρ=0.84 (healthy)
#
# The 2× service time ratio is realistic: shared-resource effects (cache, memory
# bandwidth) cause genuine per-request slowdowns proportional to antagonist load,
# even below saturation — exactly the signal Prequal probes for.
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
    --base-service-ms  50      \
    --cpu-load         10      \
    --hot-fraction     0.3     \
    --hot-cpu-load     80      \
    --qrif             0.70    \
    --duration         30s     \
    --warmup           15s     \
    --output           "$OUTPUT"

echo ""
echo "done — results in $OUTPUT"
echo "plot: python3 scripts/plot.py --input $OUTPUT --experiment a --output plots/"
