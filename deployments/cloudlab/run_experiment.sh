#!/usr/bin/env bash
# run_experiment.sh — full CloudLab experiment orchestrator.
#
# Reads node hostnames from NODES_FILE (default nodes.txt), copies binaries,
# starts backends and LB processes via SSH, runs calibration or experiment,
# and downloads the resulting CSV.
#
# Backend antagonist load uses the same dynamic random-walk model as the
# Docker dev setup (see backend/main.go and cmd/compose-gen): each backend's
# ANTAGONIST_BIAS is drawn from one of three groups (hot/neutral/cold-prone),
# split into thirds across the backend population.
#
# Usage:
#   ./run_experiment.sh [options]
#
# Options:
#   --nodes FILE          path to nodes file (default: nodes.txt)
#   --user USER           SSH username (default: $USER)
#   --ssh-key FILE        SSH private key (default: ~/.ssh/id_rsa)
#   --separate-lb         first node in NODES_FILE is LB-only (default: true)
#   --no-separate-lb      first node runs LB + backends too
#   --loadgen-node HOST   run the experiment/loadgen binary on this node
#                         instead of the LB node (recommended at high QPS so
#                         the load generator doesn't compete with the LB for
#                         CPU). Need not be listed in NODES_FILE.
#   --experiment a|b      which experiment to run (default: b)
#   --calibrate           run calibrate instead of an experiment
#   --qps N               QPS for experiment (required unless --calibrate)
#   --duration Ns         measurement window per step (default: 30s)
#   --warmup Ns           warm-up per step (default: 10s)
#   --backends N          total number of backend processes (default: one
#                         per backend node). May exceed the number of backend
#                         nodes — extra backends are packed onto nodes
#                         round-robin on incrementing ports (8080, 8081, ...).
#   --hot-bias F          ANTAGONIST_BIAS (p_up) for hot-prone backends (default 0.50)
#   --neutral-bias F      ANTAGONIST_BIAS (p_up) for neutral backends (default 0.425)
#   --cold-bias F         ANTAGONIST_BIAS (p_up) for cold-prone backends (default 0.3)
#   --mean-dwell-ms N     mean dwell time (ms) for the antagonist random walk (default 2000)
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
LOADGEN_NODE=""
EXPERIMENT="b"
DO_CALIBRATE=false
QPS=""
DURATION="30s"
WARMUP="10s"
N_BACKENDS=""
HOT_BIAS=0.50
NEUTRAL_BIAS=0.425
COLD_BIAS=0.3
MEAN_DWELL_MS=2000
CAPACITY=20
BASE_SERVICE_MS=5
PROBE_POOL_SIZE=8
PROBE_RATE_MULT=3.0
QRIF=0.84
OUTPUT="$REPO_ROOT/results.csv"
SKIP_SETUP=false
SKIP_START=false
BASE_PORT=8080

# ── arg parsing ───────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
    case "$1" in
        --nodes)           NODES_FILE="$2"; shift 2 ;;
        --user)            SSH_USER="$2"; shift 2 ;;
        --ssh-key)         SSH_KEY="$2"; shift 2 ;;
        --separate-lb)     SEPARATE_LB=true; shift ;;
        --no-separate-lb)  SEPARATE_LB=false; shift ;;
        --loadgen-node)    LOADGEN_NODE="$2"; shift 2 ;;
        --experiment)      EXPERIMENT="$2"; shift 2 ;;
        --calibrate)       DO_CALIBRATE=true; shift ;;
        --qps)             QPS="$2"; shift 2 ;;
        --duration)        DURATION="$2"; shift 2 ;;
        --warmup)          WARMUP="$2"; shift 2 ;;
        --backends)        N_BACKENDS="$2"; shift 2 ;;
        --hot-bias)        HOT_BIAS="$2"; shift 2 ;;
        --neutral-bias)    NEUTRAL_BIAS="$2"; shift 2 ;;
        --cold-bias)       COLD_BIAS="$2"; shift 2 ;;
        --mean-dwell-ms)   MEAN_DWELL_MS="$2"; shift 2 ;;
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
short_host() { echo "$1" | cut -d. -f1; }

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
    # node0 runs LB + backends too
    BACKEND_NODES=("${ALL_NODES[@]}")
fi

N_BACKEND_NODES="${#BACKEND_NODES[@]}"

if [[ -z "$N_BACKENDS" ]]; then
    N_BACKENDS="$N_BACKEND_NODES"
fi

if [[ -z "$LOADGEN_NODE" ]]; then
    LOADGEN_NODE="$LB_NODE"
fi

echo "=== CloudLab experiment setup ==="
echo "LB node      : $LB_NODE"
echo "Backend nodes: $N_BACKEND_NODES"
echo "Backends     : $N_BACKENDS total ($(awk -v n="$N_BACKENDS" -v m="$N_BACKEND_NODES" 'BEGIN{printf "%.2f", n/m}') per node avg)"
echo "Loadgen node : $LOADGEN_NODE"
echo "Separate LB  : $SEPARATE_LB"
echo ""

# ── build-check ───────────────────────────────────────────────────────────────
SETUP_NODES=("${ALL_NODES[@]}")
if [[ "$LOADGEN_NODE" != "$LB_NODE" ]]; then
    SETUP_NODES+=("$LOADGEN_NODE")
fi

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
    for node in "${SETUP_NODES[@]}"; do
        "$SCRIPT_DIR/setup.sh" "$node" "$SSH_USER" &
    done
    wait
    echo ">>> binaries copied"
fi

# ── compute per-backend placement (node, port, antagonist bias group) ─────────
# Backend i (0-indexed) is placed on node (i % N_BACKEND_NODES) using port
# BASE_PORT + (i / N_BACKEND_NODES). Bias groups follow the same thirds split
# as cmd/compose-gen: first third hot-prone, last third cold-prone, rest neutral.
N_HOT=$((N_BACKENDS / 3))
N_COLD=$((N_BACKENDS / 3))

declare -a BACKEND_HOST BACKEND_PORT BACKEND_BIAS
declare -A NODE_SPECS   # node index -> "id:port:bias id:port:bias ..."
for ((j = 0; j < N_BACKEND_NODES; j++)); do NODE_SPECS[$j]=""; done

for ((i = 0; i < N_BACKENDS; i++)); do
    node_idx=$((i % N_BACKEND_NODES))
    port=$((BASE_PORT + i / N_BACKEND_NODES))
    if (( i < N_HOT )); then
        bias="$HOT_BIAS"
    elif (( i < N_BACKENDS - N_COLD )); then
        bias="$NEUTRAL_BIAS"
    else
        bias="$COLD_BIAS"
    fi
    server_id="server-$((i+1))"
    BACKEND_HOST[$i]="$(short_host "${BACKEND_NODES[$node_idx]}")"
    BACKEND_PORT[$i]="$port"
    BACKEND_BIAS[$i]="$bias"
    NODE_SPECS[$node_idx]+="${server_id}:${port}:${bias} "
done

PER_NODE_MAX=$(( (N_BACKENDS + N_BACKEND_NODES - 1) / N_BACKEND_NODES ))
echo ">>> placement: $N_BACKENDS backends across $N_BACKEND_NODES nodes (up to $PER_NODE_MAX/node, ports $BASE_PORT-$((BASE_PORT + PER_NODE_MAX - 1)))"
echo ">>> bias groups: $N_HOT hot @ $HOT_BIAS, $((N_BACKENDS - N_HOT - N_COLD)) neutral @ $NEUTRAL_BIAS, $N_COLD cold @ $COLD_BIAS"

# ── start backends ────────────────────────────────────────────────────────────
if ! $SKIP_START; then
    echo ">>> starting backends..."
    for ((j = 0; j < N_BACKEND_NODES; j++)); do
        node="${BACKEND_NODES[$j]}"
        specs="${NODE_SPECS[$j]}"
        [[ -z "$specs" ]] && continue
        ssh_cmd "$node" \
            BACKEND_SPECS="$specs" \
            MEAN_DWELL_MS="$MEAN_DWELL_MS" \
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

    BACKEND_ENV=()
    for ((i = 0; i < N_BACKENDS; i++)); do
        BACKEND_ENV+=("BACKEND_SERVER$((i+1))=${BACKEND_HOST[$i]}:${BACKEND_PORT[$i]}")
    done

    ssh_cmd "$LB_NODE" \
        env "${BACKEND_ENV[@]}" \
        PROBE_POOL_SIZE="$PROBE_POOL_SIZE" \
        PROBE_RATE_MULTIPLIER="$PROBE_RATE_MULT" \
        QRIF="$QRIF" \
        bash < "$SCRIPT_DIR/run_lb.sh"

    echo ">>> LBs started; waiting for readiness..."
    sleep 3
fi

# ── derive LB URLs as seen from the loadgen node ──────────────────────────────
if [[ "$LOADGEN_NODE" == "$LB_NODE" ]]; then
    LB_PREQUAL_URL="http://localhost:8080"
    LB_RR_URL="http://localhost:8081"
else
    LB_HOST="$(short_host "$LB_NODE")"
    LB_PREQUAL_URL="http://${LB_HOST}:8080"
    LB_RR_URL="http://${LB_HOST}:8081"
fi

# ── calibrate or experiment ───────────────────────────────────────────────────
REMOTE_OUTPUT="/tmp/prequal-results.csv"

if $DO_CALIBRATE; then
    echo ">>> running calibration on $LOADGEN_NODE..."
    ssh_cmd "$LOADGEN_NODE" "~/prequal/calibrate \
        --lb $LB_PREQUAL_URL \
        --start-qps 10 --max-qps 500 --step 10 \
        --duration $WARMUP"
else
    if [[ -z "$QPS" ]]; then
        echo "error: --qps is required for experiment runs (run --calibrate first)"
        exit 1
    fi

    echo ">>> running experiment $EXPERIMENT at ${QPS} QPS on $LOADGEN_NODE..."
    ssh_cmd "$LOADGEN_NODE" "~/prequal/experiment \
        --experiment $EXPERIMENT \
        --qps $QPS \
        --duration $DURATION \
        --warmup $WARMUP \
        --backends $N_BACKENDS \
        --hot-bias $HOT_BIAS \
        --neutral-bias $NEUTRAL_BIAS \
        --cold-bias $COLD_BIAS \
        --mean-dwell-ms $MEAN_DWELL_MS \
        --capacity $CAPACITY \
        --base-service-ms $BASE_SERVICE_MS \
        --qrif $QRIF \
        --cloudlab \
        --lb-prequal $LB_PREQUAL_URL \
        --lb-rr      $LB_RR_URL \
        --output     $REMOTE_OUTPUT"

    echo ">>> downloading results..."
    scp_cmd "$SSH_USER@$LOADGEN_NODE:$REMOTE_OUTPUT" "$OUTPUT"
    echo ">>> results saved to $OUTPUT"
fi
