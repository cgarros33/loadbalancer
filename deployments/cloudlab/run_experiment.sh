#!/usr/bin/env bash
# run_experiment.sh — full CloudLab experiment orchestrator.
#
# Reads node hostnames from NODES_FILE (default nodes.txt), copies binaries,
# starts backends and LB processes via SSH, runs calibration or experiment,
# and downloads the resulting CSV.
#
# Usage:
#   ./run_experiment.sh [options]
#
# Options:
#   --nodes FILE          path to nodes file (default: nodes.txt)
#   --user USER           SSH username (default: $USER)
#   --ssh-key FILE        SSH private key (default: ~/.ssh/id_rsa)
#   --separate-lb         first node in NODES_FILE is LB-only (default: true)
#   --no-separate-lb      first node runs LB + one backend
#   --experiment a|b      which experiment to run (default: b)
#   --calibrate           run calibrate instead of an experiment
#   --qps N               QPS for experiment (required unless --calibrate)
#   --duration Ns         measurement window per step (default: 30s)
#   --warmup Ns           warm-up per step (default: 10s)
#   --cpu-load N          baseline (cold) CPU_LOAD antagonist fraction (default: 50)
#   --hot-fraction F      fraction of backends that are hot, e.g. 0.2 (default: 0)
#   --hot-cpu-load N      CPU_LOAD for hot backends (default: 80)
#   --capacity N          CAPACITY per backend (default: 20)
#   --base-service-ms N   BASE_SERVICE_MS per backend (default: 5)
#   --probe-pool-size N   PROBE_POOL_SIZE (default: 8)
#   --probe-rate-mult F   PROBE_RATE_MULTIPLIER (default: 3.0)
#   --qrif F              QRIF (default: 0.84)
#   --output FILE         local path for results CSV (default: results.csv)
#   --skip-setup          skip rsync of binaries (nodes already set up)
#   --skip-start          skip starting processes (already running)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
BIN_DIR="$REPO_ROOT/bin/linux"

# ── defaults ──────────────────────────────────────────────────────────────────
NODES_FILE="$SCRIPT_DIR/nodes.txt"
SSH_USER="$USER"
SSH_KEY="$HOME/.ssh/id_rsa"
SEPARATE_LB=true
EXPERIMENT="b"
DO_CALIBRATE=false
QPS=""
DURATION="30s"
WARMUP="10s"
CPU_LOAD=50
HOT_FRACTION=0
HOT_CPU_LOAD=80
CAPACITY=20
BASE_SERVICE_MS=5
PROBE_POOL_SIZE=8
PROBE_RATE_MULT=3.0
QRIF=0.84
OUTPUT="$REPO_ROOT/results.csv"
SKIP_SETUP=false
SKIP_START=false
BACKEND_PORT=8080

# ── arg parsing ───────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
    case "$1" in
        --nodes)           NODES_FILE="$2"; shift 2 ;;
        --user)            SSH_USER="$2"; shift 2 ;;
        --ssh-key)         SSH_KEY="$2"; shift 2 ;;
        --separate-lb)     SEPARATE_LB=true; shift ;;
        --no-separate-lb)  SEPARATE_LB=false; shift ;;
        --experiment)      EXPERIMENT="$2"; shift 2 ;;
        --calibrate)       DO_CALIBRATE=true; shift ;;
        --qps)             QPS="$2"; shift 2 ;;
        --duration)        DURATION="$2"; shift 2 ;;
        --warmup)          WARMUP="$2"; shift 2 ;;
        --cpu-load)        CPU_LOAD="$2"; shift 2 ;;
        --hot-fraction)    HOT_FRACTION="$2"; shift 2 ;;
        --hot-cpu-load)    HOT_CPU_LOAD="$2"; shift 2 ;;
        --capacity)        CAPACITY="$2"; shift 2 ;;
        --base-service-ms) BASE_SERVICE_MS="$2"; shift 2 ;;
        --probe-pool-size) PROBE_POOL_SIZE="$2"; shift 2 ;;
        --probe-rate-mult) PROBE_RATE_MULT="$2"; shift 2 ;;
        --qrif)            QRIF="$2"; shift 2 ;;
        --output)          OUTPUT="$2"; shift 2 ;;
        --skip-setup)      SKIP_SETUP=true; shift ;;
        --skip-start)      SKIP_START=true; shift ;;
        *) echo "unknown option: $1"; exit 1 ;;
    esac
done

ssh_cmd() { ssh -i "$SSH_KEY" -o StrictHostKeyChecking=no "$SSH_USER@$1" "${@:2}"; }
scp_cmd() { scp -i "$SSH_KEY" -o StrictHostKeyChecking=no "$@"; }

# ── read nodes ────────────────────────────────────────────────────────────────
if [[ ! -f "$NODES_FILE" ]]; then
    echo "error: nodes file not found: $NODES_FILE"
    echo "  copy nodes.txt.example to nodes.txt and fill in your node hostnames"
    exit 1
fi

mapfile -t ALL_NODES < <(grep -v '^\s*#' "$NODES_FILE" | grep -v '^\s*$')

if [[ ${#ALL_NODES[@]} -lt 2 ]]; then
    echo "error: need at least 2 nodes (1 LB + 1 backend); got ${#ALL_NODES[@]}"
    exit 1
fi

LB_NODE="${ALL_NODES[0]}"

if $SEPARATE_LB; then
    BACKEND_NODES=("${ALL_NODES[@]:1}")
else
    # node0 runs LB + one backend
    BACKEND_NODES=("${ALL_NODES[@]}")
fi

N_BACKENDS="${#BACKEND_NODES[@]}"
echo "=== CloudLab experiment setup ==="
echo "LB node    : $LB_NODE"
echo "Backends   : $N_BACKENDS nodes"
echo "Separate LB: $SEPARATE_LB"
echo ""

# ── build-check ───────────────────────────────────────────────────────────────
if ! $SKIP_SETUP; then
    if [[ ! -f "$BIN_DIR/server" ]]; then
        echo ">>> building linux/amd64 binaries..."
        cd "$REPO_ROOT"
        make build-linux
    fi
fi

# ── setup: copy binaries ──────────────────────────────────────────────────────
if ! $SKIP_SETUP; then
    echo ">>> copying binaries to all nodes..."
    for node in "${ALL_NODES[@]}"; do
        "$SCRIPT_DIR/setup.sh" "$node" "$SSH_USER" &
    done
    wait
    echo ">>> binaries copied"
fi

# ── derive internal addresses ─────────────────────────────────────────────────
# CloudLab resolves short hostnames within the experiment network.
# We take the first label of the FQDN as the internal address.
short_host() { echo "$1" | cut -d. -f1; }

# ── start backends ────────────────────────────────────────────────────────────
# Compute how many backends are "hot" from the hot-fraction.
N_HOT=$(echo "$HOT_FRACTION $N_BACKENDS" | awk '{printf "%d", int($1 * $2)}')

if ! $SKIP_START; then
    echo ">>> starting backends ($N_HOT hot @ CPU_LOAD=${HOT_CPU_LOAD}%, $((N_BACKENDS - N_HOT)) cold @ CPU_LOAD=${CPU_LOAD}%)..."
    for i in "${!BACKEND_NODES[@]}"; do
        node="${BACKEND_NODES[$i]}"
        server_id="server-$((i+1))"
        if [[ $i -lt $N_HOT ]]; then
            backend_cpu=$HOT_CPU_LOAD
        else
            backend_cpu=$CPU_LOAD
        fi
        ssh_cmd "$node" \
            SERVER_ID="$server_id" \
            PORT="$BACKEND_PORT" \
            CPU_LOAD="$backend_cpu" \
            BASE_SERVICE_MS="$BASE_SERVICE_MS" \
            CAPACITY="$CAPACITY" \
            bash < "$SCRIPT_DIR/run_backends.sh" &
    done
    wait
    echo ">>> backends started"
    sleep 2
fi

# ── start LB ─────────────────────────────────────────────────────────────────
if ! $SKIP_START; then
    echo ">>> starting LB on $LB_NODE..."

    # Build BACKEND_SERVERn=host:port env string
    BACKEND_ENV=()
    for i in "${!BACKEND_NODES[@]}"; do
        node="${BACKEND_NODES[$i]}"
        internal="${BACKEND_NODES[$i]}"
        # If the node looks like an FQDN, use just the first label internally
        if [[ "$internal" == *.* ]]; then
            internal="$(short_host "$node")"
        fi
        BACKEND_ENV+=("BACKEND_SERVER$((i+1))=${internal}:${BACKEND_PORT}")
    done

    PROBE_POOL_SIZE_VAL="$PROBE_POOL_SIZE"
    PROBE_RATE_MULT_VAL="$PROBE_RATE_MULT"
    QRIF_VAL="$QRIF"

    ssh_cmd "$LB_NODE" \
        env "${BACKEND_ENV[@]}" \
        PROBE_POOL_SIZE="$PROBE_POOL_SIZE_VAL" \
        PROBE_RATE_MULTIPLIER="$PROBE_RATE_MULT_VAL" \
        QRIF="$QRIF_VAL" \
        bash < "$SCRIPT_DIR/run_lb.sh"

    echo ">>> LBs started; waiting for readiness..."
    sleep 3
fi

# ── calibrate or experiment ───────────────────────────────────────────────────
REMOTE_OUTPUT="/tmp/prequal-results.csv"

if $DO_CALIBRATE; then
    echo ">>> running calibration on LB node..."
    ssh_cmd "$LB_NODE" "~/prequal/calibrate \
        --lb http://localhost:8080 \
        --start-qps 10 --max-qps 500 --step 10 \
        --duration $WARMUP"
else
    if [[ -z "$QPS" ]]; then
        echo "error: --qps is required for experiment runs (run --calibrate first)"
        exit 1
    fi

    echo ">>> running experiment $EXPERIMENT at ${QPS} QPS on LB node..."
    ssh_cmd "$LB_NODE" "~/prequal/experiment \
        --experiment $EXPERIMENT \
        --qps $QPS \
        --duration $DURATION \
        --warmup $WARMUP \
        --backends $N_BACKENDS \
        --cpu-load $CPU_LOAD \
        --hot-fraction $HOT_FRACTION \
        --hot-cpu-load $HOT_CPU_LOAD \
        --cloudlab \
        --lb-prequal http://localhost:8080 \
        --lb-rr     http://localhost:8081 \
        --output    $REMOTE_OUTPUT"

    echo ">>> downloading results..."
    scp_cmd "$SSH_USER@$LB_NODE:$REMOTE_OUTPUT" "$OUTPUT"
    echo ">>> results saved to $OUTPUT"
fi
