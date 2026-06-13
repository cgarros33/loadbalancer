#!/usr/bin/env bash
# setup.sh — copy pre-built Linux binaries to a CloudLab node.
# Run from the repo root after `make build-linux`.
#
# Usage: ./deployments/cloudlab/setup.sh <node_ssh_host> [ssh_user] [ssh_key]
set -euo pipefail

NODE="${1:?usage: setup.sh <node_ssh_host> [ssh_user] [ssh_key]}"
SSH_USER="${2:-$USER}"
SSH_KEY="${3:-}"
REMOTE="$SSH_USER@$NODE"
BIN_DIR="bin/linux"

SSH_OPTS=(-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes)
if [[ -n "$SSH_KEY" ]]; then
    SSH_OPTS+=(-i "$SSH_KEY")
fi

if [[ ! -f "$BIN_DIR/server" || ! -f "$BIN_DIR/backend" || ! -f "$BIN_DIR/experiment" || ! -f "$BIN_DIR/calibrate" ]]; then
    echo "error: binaries not found in $BIN_DIR — run 'make build-linux' first"
    exit 1
fi

echo ">>> copying binaries to $REMOTE"
ssh "${SSH_OPTS[@]}" "$REMOTE" "mkdir -p ~/prequal"
rsync -avz --progress -e "ssh ${SSH_OPTS[*]}" \
    "$BIN_DIR/server" \
    "$BIN_DIR/backend" \
    "$BIN_DIR/experiment" \
    "$BIN_DIR/calibrate" \
    "$REMOTE:~/prequal/"

ssh "${SSH_OPTS[@]}" "$REMOTE" "chmod +x ~/prequal/*"
echo ">>> done: $NODE"
