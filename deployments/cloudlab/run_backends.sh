#!/usr/bin/env bash
# run_backends.sh — start one or more backend processes on this node.
# Called via SSH by run_experiment.sh; all config comes from env vars.
#
# Required env: BACKEND_SPECS — space-separated "server_id:port:bias" entries,
#   one per backend process to run on this node (e.g.
#   "server-1:8080:0.5 server-2:8081:0.425").
# Optional env: MEAN_DWELL_MS (default 2000), BASE_SERVICE_MS (default 5),
#   CAPACITY (default 20)
#
# Each entry's "bias" is ANTAGONIST_BIAS (p_up) for that backend's antagonist
# random walk — see backend/main.go and cmd/compose-gen for the model.
set -euo pipefail

BACKEND_SPECS="${BACKEND_SPECS:?BACKEND_SPECS not set}"
MEAN_DWELL_MS="${MEAN_DWELL_MS:-2000}"
BASE_SERVICE_MS="${BASE_SERVICE_MS:-5}"
CAPACITY="${CAPACITY:-20}"

pkill -f "prequal/backend" 2>/dev/null || true
sleep 0.5

CORE_ID=0
for spec in $BACKEND_SPECS; do
    IFS=: read -r server_id port bias <<< "$spec"
    LOG="$HOME/prequal/backend-${server_id}.log"

    nohup env \
        SERVER_ID="$server_id" \
        PORT="$port" \
        ANTAGONIST_BIAS="$bias" \
        MEAN_DWELL_MS="$MEAN_DWELL_MS" \
        BASE_SERVICE_MS="$BASE_SERVICE_MS" \
        CAPACITY="$CAPACITY" \
        taskset -c $CORE_ID \
        ~/prequal/backend >"$LOG" 2>&1 &

    echo "backend $server_id started on :$port (bias=$bias, core=$CORE_ID, pid $!), log: $LOG"
    CORE_ID=$((CORE_ID + 1))
done
