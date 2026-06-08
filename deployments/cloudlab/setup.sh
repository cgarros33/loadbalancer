#!/usr/bin/env bash
# setup.sh — copy pre-built Linux binaries to a CloudLab node.
# Run from the repo root after `make build-linux`.
#
# Usage: ./deployments/cloudlab/setup.sh <node_ssh_host> [ssh_user]
set -euo pipefail

NODE="${1:?usage: setup.sh <node_ssh_host> [ssh_user]}"
SSH_USER="${2:-$USER}"
REMOTE="$SSH_USER@$NODE"
BIN_DIR="bin/linux"

if [[ ! -f "$BIN_DIR/server" || ! -f "$BIN_DIR/backend" || ! -f "$BIN_DIR/experiment" || ! -f "$BIN_DIR/calibrate" ]]; then
    echo "error: binaries not found in $BIN_DIR — run 'make build-linux' first"
    exit 1
fi

echo ">>> copying binaries to $REMOTE"
ssh "$REMOTE" "mkdir -p ~/prequal"
rsync -avz --progress \
    "$BIN_DIR/server" \
    "$BIN_DIR/backend" \
    "$BIN_DIR/experiment" \
    "$BIN_DIR/calibrate" \
    "$REMOTE:~/prequal/"

ssh "$REMOTE" "chmod +x ~/prequal/*"
echo ">>> done: $NODE"
