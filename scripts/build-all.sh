#!/bin/bash
set -e
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TAG="${1:-latest}"

echo "=== Building all hosting-panel images (tag: ${TAG}) ==="
echo ""

echo "--- 1/3: hosting-operator ---"
"${SCRIPT_DIR}/build-operator.sh" "${TAG}"
echo ""

echo "--- 2/3: panel-core ---"
"${SCRIPT_DIR}/build-backend.sh" "${TAG}"
echo ""

echo "--- 3/3: panel-ui ---"
"${SCRIPT_DIR}/build-frontend.sh" "${TAG}"
echo ""

echo "=== All images built and imported into k3s ==="
k3s ctr images list | grep hosting-panel
