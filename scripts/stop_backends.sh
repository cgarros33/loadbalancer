#!/usr/bin/env bash
set -euo pipefail
if [[ -f /tmp/backend_pids ]]; then
    read -r HOT_PID COLD_PID < /tmp/backend_pids
    kill "$HOT_PID" "$COLD_PID" 2>/dev/null && echo "stopped $HOT_PID $COLD_PID" || true
    rm /tmp/backend_pids
else
    # fallback: kill by port
    for port in 9001 9002; do
        pid=$(lsof -ti tcp:"$port" 2>/dev/null || true)
        [[ -n "$pid" ]] && kill "$pid" && echo "killed :$port (pid $pid)" || true
    done
fi
