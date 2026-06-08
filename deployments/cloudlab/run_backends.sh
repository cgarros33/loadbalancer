#!/usr/bin/env bash
# run_backends.sh — start a backend process on this node.
# Called via SSH by run_experiment.sh; all config comes from env vars.
#
# Required env: SERVER_ID, PORT
# Optional env: CPU_LOAD (default 50), BASE_SERVICE_MS (default 5), CAPACITY (default 20)
set -euo pipefail

SERVER_ID="${SERVER_ID:?SERVER_ID not set}"
PORT="${PORT:-8080}"
CPU_LOAD="${CPU_LOAD:-50}"
BASE_SERVICE_MS="${BASE_SERVICE_MS:-5}"
CAPACITY="${CAPACITY:-20}"
LOG="$HOME/prequal/backend-${SERVER_ID}.log"

pkill -f "prequal/backend" 2>/dev/null || true
sleep 0.5

nohup env \
    SERVER_ID="$SERVER_ID" \
    PORT="$PORT" \
    CPU_LOAD="$CPU_LOAD" \
    BASE_SERVICE_MS="$BASE_SERVICE_MS" \
    CAPACITY="$CAPACITY" \
    ~/prequal/backend >"$LOG" 2>&1 &

echo "backend $SERVER_ID started (pid $!), log: $LOG"
