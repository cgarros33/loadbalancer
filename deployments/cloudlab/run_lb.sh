#!/usr/bin/env bash
# run_lb.sh — start prequal and round-robin LB instances on this node.
# Called via SSH by run_experiment.sh; backend list comes from env vars.
#
# Required env: BACKEND_SERVER1, BACKEND_SERVER2, ... (at least one)
# Optional env: QRIF, PROBE_POOL_SIZE, PROBE_RATE_MULTIPLIER
set -euo pipefail

QRIF="${QRIF:-0.84}"
PROBE_POOL_SIZE="${PROBE_POOL_SIZE:-8}"
PROBE_RATE_MULTIPLIER="${PROBE_RATE_MULTIPLIER:-3.0}"
LOG_PREQUAL="$HOME/prequal/lb-prequal.log"
LOG_RR="$HOME/prequal/lb-rr.log"

pkill -f "prequal/server" 2>/dev/null || true
sleep 0.5

# Build the backend env string — inherit all BACKEND_SERVERn vars from caller.
BACKEND_VARS=()
i=1
while true; do
    var="BACKEND_SERVER${i}"
    val="${!var:-}"
    [[ -z "$val" ]] && break
    BACKEND_VARS+=("${var}=${val}")
    ((i++))
done

if [[ ${#BACKEND_VARS[@]} -eq 0 ]]; then
    echo "error: no BACKEND_SERVERn variables set"
    exit 1
fi

echo "starting prequal LB on :8080 with ${#BACKEND_VARS[@]} backends"
nohup env \
    "${BACKEND_VARS[@]}" \
    LB_ALGORITHM=prequal \
    QRIF="$QRIF" \
    PROBE_POOL_SIZE="$PROBE_POOL_SIZE" \
    PROBE_RATE_MULTIPLIER="$PROBE_RATE_MULTIPLIER" \
    ~/prequal/server --port 8080 >"$LOG_PREQUAL" 2>&1 &

echo "starting round-robin LB on :8081 with ${#BACKEND_VARS[@]} backends"
nohup env \
    "${BACKEND_VARS[@]}" \
    LB_ALGORITHM=roundrobin \
    QRIF="$QRIF" \
    PROBE_POOL_SIZE="$PROBE_POOL_SIZE" \
    PROBE_RATE_MULTIPLIER="$PROBE_RATE_MULTIPLIER" \
    ~/prequal/server --port 8081 >"$LOG_RR" 2>&1 &

echo "LBs started — logs: $LOG_PREQUAL, $LOG_RR"
