#!/usr/bin/env bash
# Experiment B — vary probe pool size at ~75% load (Figure 9 replica).
#
# Each algorithm runs in its own isolated compose lifecycle (matching the paper).
# Same parameters as Experiment A for consistency.
#
# Usage: ./scripts/run_experiment_b.sh [OUTPUT]
#   OUTPUT  CSV file path (default: results_b.csv)

set -euo pipefail

OUTPUT=${1:-results_b.csv}
BINARY=./bin/experiment

if [[ ! -x "$BINARY" ]]; then
    echo "binary not found — run: make build" >&2
    exit 1
fi

echo "=== Experiment B: probe pool size sweep at 75% allocation ==="
echo "output: $OUTPUT"
echo ""

"$BINARY" \
    --experiment       b       \
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
echo "plot: python3 scripts/plot.py --input $OUTPUT --experiment b --output plots/"
