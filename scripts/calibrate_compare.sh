#!/usr/bin/env bash
# Runs calibrate against hot and cold backends sequentially and merges
# the output into a single CSV for comparison plotting.
#
# Usage: ./scripts/calibrate_compare.sh [START_QPS] [MAX_QPS] [STEP] [DURATION]
#   START_QPS  default 100
#   MAX_QPS    default 1500
#   STEP       default 100
#   DURATION   per-step measurement window, default 15s

set -euo pipefail

START=${1:-100}
MAX=${2:-1500}
STEP=${3:-100}
DUR=${4:-15s}
WARMUP=5s
BINARY=./bin/calibrate
OUT_HOT=/tmp/calib_hot.csv
OUT_COLD=/tmp/calib_cold.csv
OUT_MERGED=calibrate_compare.csv

if [[ ! -x "$BINARY" ]]; then
    echo "binary not found — run: go build -o bin/calibrate ./cmd/calibrate/" >&2
    exit 1
fi

echo "=== calibrating HOT backend (port 9001, CPU_LOAD=80) ==="
"$BINARY" \
    --lb        http://localhost:9001 \
    --start-qps "$START" \
    --max-qps   "$MAX" \
    --step      "$STEP" \
    --duration  "$DUR" \
    --warmup    "$WARMUP" \
    --threshold 99 \
    2>/dev/null > "$OUT_HOT"

echo ""
echo "=== calibrating COLD backend (port 9002, CPU_LOAD=20) ==="
"$BINARY" \
    --lb        http://localhost:9002 \
    --start-qps "$START" \
    --max-qps   "$MAX" \
    --step      "$STEP" \
    --duration  "$DUR" \
    --warmup    "$WARMUP" \
    --threshold 99 \
    2>/dev/null > "$OUT_COLD"

# Merge: add a "backend" column to each and combine.
echo "backend,qps,p50_ms,p99_ms,p999_ms,errors" > "$OUT_MERGED"
tail -n +2 "$OUT_HOT"  | sed 's/^/hot,/'  >> "$OUT_MERGED"
tail -n +2 "$OUT_COLD" | sed 's/^/cold,/' >> "$OUT_MERGED"

echo ""
echo "merged output: $OUT_MERGED"
cat "$OUT_MERGED"
