#!/bin/bash
# =============================================================================
# Deploy latest images to k3s and upgrade Helm release
# =============================================================================
# Usage: ./deploy-latest.sh [tag]
# Runs ON the k3s server (after sync.sh copies files there)
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
TAG="${1:-latest}"

HELM_RELEASE="hosting-panel"
HELM_NAMESPACE="hosting-system"
CHART_DIR="${PROJECT_DIR}/helm-chart"
VALUES_FILE="${CHART_DIR}/values-k3s-test.yaml"

GREEN='\033[0;32m'
CYAN='\033[0;36m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

info()    { echo -e "${CYAN}[INFO]${NC} $*"; }
success() { echo -e "${GREEN}[OK]${NC}   $*"; }
warn()    { echo -e "${YELLOW}[WARN]${NC} $*"; }
error()   { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }

echo ""
echo "============================================="
echo "  Hosting Panel — Deploy (tag: ${TAG})"
echo "============================================="
echo ""

# ---- Build images ----
info "Building all images..."
"${SCRIPT_DIR}/build-all.sh" "${TAG}"
echo ""

# ---- Helm upgrade ----
info "Running helm upgrade..."

if ! helm status "${HELM_RELEASE}" -n "${HELM_NAMESPACE}" &>/dev/null; then
    warn "No existing release, installing fresh..."
fi

helm upgrade --install "${HELM_RELEASE}" "${CHART_DIR}" \
    --namespace "${HELM_NAMESPACE}" \
    --create-namespace \
    -f "${VALUES_FILE}" \
    --set "panel.image.tag=${TAG}" \
    --set "panel.image.pullPolicy=Never" \
    --set "panel.ui.image.tag=${TAG}" \
    --set "panel.ui.image.pullPolicy=Never" \
    --set "operator.image.tag=${TAG}" \
    --set "operator.image.pullPolicy=Never" \
    --timeout 5m \
    --wait

success "Helm upgrade complete"
echo ""

# ---- Restart deployments to pick up new images ----
info "Restarting deployments..."
for dep in $(kubectl get deployments -n "${HELM_NAMESPACE}" -o name 2>/dev/null | grep -E "panel-core|operator|panel-ui"); do
    kubectl rollout restart "$dep" -n "${HELM_NAMESPACE}" 2>/dev/null || true
done

sleep 5

# Wait for panel-core
info "Waiting for panel-core rollout..."
kubectl rollout status deployment -l app=hosting-panel-core -n "${HELM_NAMESPACE}" --timeout=120s 2>/dev/null || \
    kubectl rollout status deployment -n "${HELM_NAMESPACE}" --timeout=120s 2>/dev/null || \
    warn "Rollout status check timed out"

echo ""
info "Pod status:"
kubectl get pods -n "${HELM_NAMESPACE}" --sort-by=.metadata.name
echo ""

success "Deploy complete!"
