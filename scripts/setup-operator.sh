#!/bin/bash
# Initialize hosting-operator with Kubebuilder inside a Go container
# Run this on the k3s machine after sync
# Usage: ./scripts/setup-operator.sh
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
OPERATOR_DIR="${PROJECT_DIR}/hosting-operator"

# Check if already initialized
if [ -f "${OPERATOR_DIR}/PROJECT" ]; then
    echo "hosting-operator already initialized (PROJECT file exists), skipping init."
    echo "To re-init, remove ${OPERATOR_DIR} and re-sync."
    exit 0
fi

echo "=== Initializing hosting-operator with Kubebuilder ==="

docker run --rm \
    -v "${OPERATOR_DIR}:/workspace" \
    -w /workspace \
    golang:latest bash -c '
        # Install kubebuilder
        curl -L -o /usr/local/bin/kubebuilder \
            https://go.kubebuilder.io/dl/latest/linux/amd64 2>/dev/null
        chmod +x /usr/local/bin/kubebuilder

        echo "Kubebuilder version:"
        kubebuilder version

        # Init project
        kubebuilder init \
            --domain hosting.panel \
            --repo github.com/hosting-panel/hosting-operator \
            --project-name hosting-operator

        echo "=== Kubebuilder init complete ==="
    '

echo "=== hosting-operator initialized ==="
ls -la "${OPERATOR_DIR}"
