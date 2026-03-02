#!/bin/bash
# Sync local hosting-panel project to k3s remote machine
# Usage: ./scripts/sync.sh [ssh-host]
# Default ssh-host: k3s
set -e

SSH_HOST="${1:-k3s}"
REMOTE_DIR="/root/hosting-panel"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

echo "=== Syncing hosting-panel to ${SSH_HOST}:${REMOTE_DIR} ==="

# Sync panel-core (excluding nothing special)
echo "--- panel-core ---"
rsync -avz --delete \
    --exclude '.git' \
    "${PROJECT_DIR}/panel-core/" "${SSH_HOST}:${REMOTE_DIR}/panel-core/"

# Sync panel-ui (excluding node_modules — will be installed in Docker build)
echo "--- panel-ui ---"
rsync -avz --delete \
    --exclude 'node_modules' \
    --exclude '.angular' \
    --exclude 'dist' \
    --exclude '.git' \
    "${PROJECT_DIR}/panel-ui/" "${SSH_HOST}:${REMOTE_DIR}/panel-ui/"

# Sync hosting-operator (excluding bin, vendor cache)
echo "--- hosting-operator ---"
rsync -avz --delete \
    --exclude 'bin' \
    --exclude '.git' \
    "${PROJECT_DIR}/hosting-operator/" "${SSH_HOST}:${REMOTE_DIR}/hosting-operator/"

# Sync helm-chart
echo "--- helm-chart ---"
rsync -avz --delete \
    --exclude 'charts' \
    --exclude '.git' \
    "${PROJECT_DIR}/helm-chart/" "${SSH_HOST}:${REMOTE_DIR}/helm-chart/"

# Sync scripts
echo "--- scripts ---"
rsync -avz --delete \
    "${PROJECT_DIR}/scripts/" "${SSH_HOST}:${REMOTE_DIR}/scripts/"
ssh "${SSH_HOST}" "chmod +x ${REMOTE_DIR}/scripts/*.sh"

# Sync root-level files (install.sh, uninstall.sh, etc.)
echo "--- root files ---"
for f in install.sh uninstall.sh; do
    if [[ -f "${PROJECT_DIR}/${f}" ]]; then
        rsync -avz "${PROJECT_DIR}/${f}" "${SSH_HOST}:${REMOTE_DIR}/${f}"
    fi
done

echo "=== Sync complete ==="
