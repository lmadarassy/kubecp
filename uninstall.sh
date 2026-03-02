#!/usr/bin/env bash
# =============================================================================
# Hosting Panel — Uninstall Script
# =============================================================================
# Removes all platform components, Helm releases, CRDs, PVCs, and namespaces.
#
# Usage:
#   ./uninstall.sh                  Interactive (with confirmation prompts)
#   ./uninstall.sh --yes            Skip confirmations (non-interactive)
#   ./uninstall.sh --keep-data      Remove platform but keep persistent data
#
# =============================================================================

set -euo pipefail

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------
readonly SCRIPT_VERSION="0.1.0"
readonly LOG_FILE="/var/log/panel-uninstall.log"
readonly HELM_RELEASE_NAME="hosting-panel"
readonly HELM_NAMESPACE="hosting-system"

# ---------------------------------------------------------------------------
# Colors
# ---------------------------------------------------------------------------
readonly RED='\033[0;31m'
readonly GREEN='\033[0;32m'
readonly YELLOW='\033[1;33m'
readonly BLUE='\033[0;34m'
readonly CYAN='\033[0;36m'
readonly BOLD='\033[1m'
readonly NC='\033[0m'

# ---------------------------------------------------------------------------
# Options
# ---------------------------------------------------------------------------
SKIP_CONFIRM=false
KEEP_DATA=false

# ---------------------------------------------------------------------------
# Logging
# ---------------------------------------------------------------------------
init_log() {
    local log_dir
    log_dir="$(dirname "$LOG_FILE")"
    [[ -d "$log_dir" ]] || mkdir -p "$log_dir" 2>/dev/null || true
    : > "$LOG_FILE" 2>/dev/null || true
    log "INFO" "Hosting Panel Uninstall Script v${SCRIPT_VERSION} started"
    log "INFO" "Date: $(date -u '+%Y-%m-%d %H:%M:%S UTC')"
}

log() {
    local level="$1"; shift
    echo "[$(date -u '+%Y-%m-%d %H:%M:%S')] [${level}] $*" >> "$LOG_FILE" 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# Output helpers
# ---------------------------------------------------------------------------
info()    { echo -e "${BLUE}[INFO]${NC} $*";    log "INFO" "$*"; }
success() { echo -e "${GREEN}[OK]${NC}   $*";   log "OK"   "$*"; }
warn()    { echo -e "${YELLOW}[WARN]${NC} $*";  log "WARN" "$*"; }
error()   { echo -e "${RED}[ERROR]${NC} $*";    log "ERROR" "$*"; }
fatal()   { error "$*"; exit 1; }
header()  { echo -e "\n${BOLD}${CYAN}==> $*${NC}"; log "STEP" "$*"; }

# ---------------------------------------------------------------------------
# CLI argument parsing
# ---------------------------------------------------------------------------
parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --yes|-y)        SKIP_CONFIRM=true; shift ;;
            --keep-data)     KEEP_DATA=true;    shift ;;
            --help|-h)       show_help; exit 0 ;;
            *)               fatal "Unknown argument: $1. Use --help for usage." ;;
        esac
    done
}

show_help() {
    cat <<'EOF'
Hosting Panel — Uninstall Script

Usage:
  ./uninstall.sh                  Interactive (with confirmation prompts)
  ./uninstall.sh --yes            Skip all confirmations
  ./uninstall.sh --keep-data      Remove platform but keep persistent volumes/data

Options:
  --yes, -y       Skip confirmation prompts
  --keep-data     Keep Longhorn volumes and database data
  --help, -h      Show this help
EOF
}

# ---------------------------------------------------------------------------
# Confirmation prompt
# ---------------------------------------------------------------------------
confirm() {
    local msg="$1"
    if [[ "$SKIP_CONFIRM" == "true" ]]; then
        info "Auto-confirmed: ${msg}"
        return 0
    fi
    echo ""
    echo -e "${YELLOW}${BOLD}WARNING:${NC} ${msg}"
    read -rp "$(echo -e "${RED}?${NC} Are you sure? Type 'yes' to confirm: ")" answer
    if [[ "$answer" != "yes" ]]; then
        info "Cancelled."
        return 1
    fi
    return 0
}

# ===========================================================================
# Uninstall steps
# ===========================================================================

# ---------------------------------------------------------------------------
# Remove Helm release
# ---------------------------------------------------------------------------
remove_helm_release() {
    header "Removing Helm release"

    if helm status "$HELM_RELEASE_NAME" -n "$HELM_NAMESPACE" &>/dev/null 2>&1; then
        info "Uninstalling Helm release: ${HELM_RELEASE_NAME}"
        helm uninstall "$HELM_RELEASE_NAME" -n "$HELM_NAMESPACE" --wait --timeout 10m 2>&1 | tee -a "$LOG_FILE"
        success "Helm release removed"
    else
        info "Helm release ${HELM_RELEASE_NAME} not found — skipping"
    fi
}

# ---------------------------------------------------------------------------
# Remove CRDs
# ---------------------------------------------------------------------------
remove_crds() {
    header "Removing Custom Resource Definitions"

    local crds=(
        "websites.hosting.panel"
        "databases.hosting.panel"
        "emailaccounts.hosting.panel"
        "sftpaccounts.hosting.panel"
        "hostingplans.hosting.panel"
    )

    for crd in "${crds[@]}"; do
        if kubectl get crd "$crd" &>/dev/null 2>&1; then
            info "Deleting CRD: ${crd}"
            kubectl delete crd "$crd" --timeout=60s 2>&1 | tee -a "$LOG_FILE" || warn "Failed to delete CRD: ${crd}"
            success "CRD removed: ${crd}"
        else
            info "CRD not found: ${crd} — skipping"
        fi
    done

    # Also remove cert-manager CRDs if they were installed by the chart
    local cm_crds
    cm_crds="$(kubectl get crds -o name 2>/dev/null | grep 'cert-manager.io' || true)"
    if [[ -n "$cm_crds" ]]; then
        info "Removing cert-manager CRDs"
        echo "$cm_crds" | while IFS= read -r crd; do
            kubectl delete "$crd" --timeout=60s 2>&1 | tee -a "$LOG_FILE" || true
        done
        success "cert-manager CRDs removed"
    fi

    # Remove Longhorn CRDs
    local lh_crds
    lh_crds="$(kubectl get crds -o name 2>/dev/null | grep 'longhorn.io' || true)"
    if [[ -n "$lh_crds" ]]; then
        info "Removing Longhorn CRDs"
        echo "$lh_crds" | while IFS= read -r crd; do
            kubectl delete "$crd" --timeout=120s 2>&1 | tee -a "$LOG_FILE" || true
        done
        success "Longhorn CRDs removed"
    fi

    # Remove MetalLB CRDs
    local mlb_crds
    mlb_crds="$(kubectl get crds -o name 2>/dev/null | grep 'metallb.io' || true)"
    if [[ -n "$mlb_crds" ]]; then
        info "Removing MetalLB CRDs"
        echo "$mlb_crds" | while IFS= read -r crd; do
            kubectl delete "$crd" --timeout=60s 2>&1 | tee -a "$LOG_FILE" || true
        done
        success "MetalLB CRDs removed"
    fi

    # Remove Contour CRDs (HTTPProxy etc.)
    local contour_crds
    contour_crds="$(kubectl get crds -o name 2>/dev/null | grep 'projectcontour.io' || true)"
    if [[ -n "$contour_crds" ]]; then
        info "Removing Contour CRDs"
        echo "$contour_crds" | while IFS= read -r crd; do
            kubectl delete "$crd" --timeout=60s 2>&1 | tee -a "$LOG_FILE" || true
        done
        success "Contour CRDs removed"
    fi
}

# ---------------------------------------------------------------------------
# Remove PVCs (persistent data)
# ---------------------------------------------------------------------------
remove_pvcs() {
    if [[ "$KEEP_DATA" == "true" ]]; then
        warn "Keeping persistent data (--keep-data flag set)"
        return 0
    fi

    header "Removing PersistentVolumeClaims"

    if ! confirm "This will permanently delete ALL persistent data (Longhorn volumes, database data, email data, website files)."; then
        warn "Skipping PVC removal — persistent data preserved"
        return 0
    fi

    # Remove PVCs in hosting-system namespace
    local pvcs
    pvcs="$(kubectl get pvc -n "$HELM_NAMESPACE" -o name 2>/dev/null || true)"
    if [[ -n "$pvcs" ]]; then
        info "Deleting PVCs in ${HELM_NAMESPACE}"
        echo "$pvcs" | while IFS= read -r pvc; do
            kubectl delete "$pvc" -n "$HELM_NAMESPACE" --timeout=120s 2>&1 | tee -a "$LOG_FILE" || true
        done
        success "PVCs removed from ${HELM_NAMESPACE}"
    fi

    # Remove PVCs in user namespaces
    local user_namespaces
    user_namespaces="$(kubectl get namespaces -o name 2>/dev/null | grep 'hosting-user-' || true)"
    if [[ -n "$user_namespaces" ]]; then
        echo "$user_namespaces" | while IFS= read -r ns; do
            local ns_name="${ns#namespace/}"
            local ns_pvcs
            ns_pvcs="$(kubectl get pvc -n "$ns_name" -o name 2>/dev/null || true)"
            if [[ -n "$ns_pvcs" ]]; then
                info "Deleting PVCs in ${ns_name}"
                echo "$ns_pvcs" | while IFS= read -r pvc; do
                    kubectl delete "$pvc" -n "$ns_name" --timeout=120s 2>&1 | tee -a "$LOG_FILE" || true
                done
            fi
        done
        success "User namespace PVCs removed"
    fi
}

# ---------------------------------------------------------------------------
# Remove namespaces
# ---------------------------------------------------------------------------
remove_namespaces() {
    header "Removing namespaces"

    # Remove user namespaces
    local user_namespaces
    user_namespaces="$(kubectl get namespaces -o name 2>/dev/null | grep 'hosting-user-' || true)"
    if [[ -n "$user_namespaces" ]]; then
        echo "$user_namespaces" | while IFS= read -r ns; do
            local ns_name="${ns#namespace/}"
            info "Deleting namespace: ${ns_name}"
            kubectl delete namespace "$ns_name" --timeout=120s 2>&1 | tee -a "$LOG_FILE" || warn "Failed to delete namespace: ${ns_name}"
        done
        success "User namespaces removed"
    fi

    # Remove hosting-system namespace
    if kubectl get namespace "$HELM_NAMESPACE" &>/dev/null 2>&1; then
        info "Deleting namespace: ${HELM_NAMESPACE}"
        kubectl delete namespace "$HELM_NAMESPACE" --timeout=300s 2>&1 | tee -a "$LOG_FILE" || warn "Failed to delete namespace: ${HELM_NAMESPACE}"
        success "Namespace ${HELM_NAMESPACE} removed"
    else
        info "Namespace ${HELM_NAMESPACE} not found — skipping"
    fi
}

# ===========================================================================
# Summary
# ===========================================================================
print_summary() {
    echo ""
    echo -e "${BOLD}${GREEN}=============================================${NC}"
    echo -e "${BOLD}${GREEN}  Hosting Panel — Uninstall Complete${NC}"
    echo -e "${BOLD}${GREEN}=============================================${NC}"
    echo ""
    if [[ "$KEEP_DATA" == "true" ]]; then
        echo -e "  ${YELLOW}Persistent data was preserved (--keep-data).${NC}"
        echo -e "  PVCs and volumes remain on the cluster."
    else
        echo -e "  All platform components and data have been removed."
    fi
    echo ""
    echo -e "  ${BOLD}Log File:${NC} ${LOG_FILE}"
    echo ""
    log "INFO" "Uninstall completed successfully"
}

# ===========================================================================
# Main
# ===========================================================================
main() {
    parse_args "$@"
    init_log

    echo ""
    echo -e "${BOLD}${RED}╔═══════════════════════════════════════════╗${NC}"
    echo -e "${BOLD}${RED}║  Hosting Panel — Uninstall Script v${SCRIPT_VERSION}  ║${NC}"
    echo -e "${BOLD}${RED}╚═══════════════════════════════════════════╝${NC}"
    echo ""

    # Check for kubectl
    if ! command -v kubectl &>/dev/null; then
        fatal "kubectl not found. Cannot proceed with uninstall."
    fi

    # Check for helm
    if ! command -v helm &>/dev/null; then
        fatal "helm not found. Cannot proceed with uninstall."
    fi

    # Initial confirmation
    if ! confirm "This will remove the Hosting Panel platform from your cluster."; then
        info "Uninstall cancelled."
        exit 0
    fi

    # Step 1: Remove Helm release
    remove_helm_release

    # Step 2: Remove PVCs (with separate confirmation for data deletion)
    remove_pvcs

    # Step 3: Remove CRDs
    remove_crds

    # Step 4: Remove namespaces
    remove_namespaces

    # Summary
    print_summary
}

main "$@"
