#!/bin/bash
# =============================================================================
# REST API Integration Test — runs ON the k3s server
# =============================================================================
# Usage: ./rest-api-test.sh [base_url]
# Default: auto-detects panel-core ClusterIP in hosting-system namespace
# =============================================================================
set -euo pipefail

NAMESPACE="hosting-system"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

PASS=0; FAIL=0; SKIP=0; TOTAL=0

# ---- Helpers ----
log_test() {
    local name="$1" method="$2" url="$3" expected="$4" actual="$5"
    ((TOTAL++))
    if [[ "$actual" == "$expected" ]]; then
        echo -e "  ${GREEN}✓${NC} ${name} [${method}] → ${actual}"
        ((PASS++))
    else
        echo -e "  ${RED}✗${NC} ${name} [${method}] → expected ${expected}, got ${actual}"
        ((FAIL++))
    fi
}

log_test_range() {
    local name="$1" method="$2" url="$3" min="$4" max="$5" actual="$6"
    ((TOTAL++))
    if [[ "$actual" -ge "$min" ]] 2>/dev/null && [[ "$actual" -le "$max" ]] 2>/dev/null; then
        echo -e "  ${GREEN}✓${NC} ${name} [${method}] → ${actual}"
        ((PASS++))
    else
        echo -e "  ${RED}✗${NC} ${name} [${method}] → expected ${min}-${max}, got ${actual}"
        ((FAIL++))
    fi
}

skip_test() {
    local name="$1" reason="$2"
    ((TOTAL++)); ((SKIP++))
    echo -e "  ${YELLOW}⊘${NC} ${name} — skipped: ${reason}"
}

api() {
    local method="$1" path="$2"; shift 2
    curl -s -o /tmp/api_resp -w "%{http_code}" \
        -X "$method" \
        -H "Authorization: Bearer ${TOKEN:-}" \
        -H "Content-Type: application/json" \
        "$@" "${BASE_URL}${path}" 2>/dev/null || echo "000"
}

body() { cat /tmp/api_resp 2>/dev/null || echo "{}"; }

echo ""
echo "============================================="
echo "  Hosting Panel — REST API Test"
echo "============================================="
echo ""
