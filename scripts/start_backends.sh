#!/usr/bin/env bash
# Launches a hot and a cold backend for side-by-side comparison.
# Hot  (port 9001): CPU_LOAD=80 — saturates early
# Cold (port 9002): CPU_LOAD=20 — stays flat much longer
#
# Usage: ./scripts/start_backends.sh [CAPACITY] [BASE_MS]
#   CAPACITY   concurrency slots per backend (default 20)
#   BASE_MS    base service time in ms      (default 5)

set -euo pipefail

CAPACITY=${1:-20}
BASE_MS=${2:-5}
BINARY=./bin/backend

if [[ ! -x "$BINARY" ]]; then
    echo "binary not found — run: go build -o bin/backend ./backend/" >&2
    exit 1
fi

# Kill any previous instances on these ports.
for port in 9001 9002; do
    pid=$(lsof -ti tcp:"$port" 2>/dev/null || true)
    if [[ -n "$pid" ]]; then
        echo "killing existing process on :$port (pid $pid)"
        kill "$pid" 2>/dev/null || true
        sleep 0.3
    fi
done

echo "starting hot  backend on :9001  CPU_LOAD=80 CAPACITY=$CAPACITY BASE_MS=$BASE_MS"
PORT=9001 SERVER_ID=hot  CPU_LOAD=80 CAPACITY="$CAPACITY" BASE_SERVICE_MS="$BASE_MS" \
    "$BINARY" &> /tmp/backend_hot.log &
HOT_PID=$!

echo "starting cold backend on :9002  CPU_LOAD=20 CAPACITY=$CAPACITY BASE_MS=$BASE_MS"
PORT=9002 SERVER_ID=cold CPU_LOAD=20 CAPACITY="$CAPACITY" BASE_SERVICE_MS="$BASE_MS" \
    "$BINARY" &> /tmp/backend_cold.log &
COLD_PID=$!

# Wait for both to be listening.
for port in 9001 9002; do
    for i in $(seq 1 20); do
        if curl -sf "http://localhost:$port/health" &>/dev/null; then
            echo ":$port ready"
            break
        fi
        sleep 0.2
    done
done

echo ""
echo "backends running — hot=$HOT_PID cold=$COLD_PID"
echo "logs: /tmp/backend_hot.log  /tmp/backend_cold.log"
echo ""
echo "to stop:  kill $HOT_PID $COLD_PID"
echo "          or: ./scripts/stop_backends.sh"

echo "$HOT_PID $COLD_PID" > /tmp/backend_pids
