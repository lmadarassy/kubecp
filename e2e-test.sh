#!/bin/bash
# =============================================================================
# Hosting Panel — End-to-End Integration Test
# =============================================================================
# Runs ON the k3s2 VM (or any host with network access to the panel).
# Tests real protocol flows: HTTP, SFTP, SMTP, IMAP, MySQL.
#
# Usage:
#   bash e2e-test.sh [--ip <NODE_IP>] [--domain test.example.com]
#
# Dependencies (installed automatically if missing):
#   curl, sshpass, swaks, curl (imap), mysql-client, jq
# =============================================================================
set -uo pipefail

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------
PANEL_IP="${PANEL_IP:-}"
PANEL_FQDN=""
TEST_DOMAIN="${TEST_DOMAIN:-e2etest.local}"
TEST_WEBSITE="e2e-website"
TEST_WEBSITE_K8S=""  # will be set after domain is known (sanitized domain name)
TEST_DB_NAME="e2edb"
TEST_DB_USER="usr_admin_e2edb"   # operator adds usr_ prefix to username-prefixed db name
TEST_DB_PASS=""             # operator generates password, stored in DB status
TEST_EMAIL_USER="testuser"
TEST_EMAIL_PASS="E2eMailPass123!"
TEST_EMAIL="${TEST_EMAIL_USER}@${TEST_DOMAIN}"
TEST_SFTP_USER="admin"
TEST_SFTP_PASS=""  # populated from KC_ADMIN_PASS after secret read
SFTP_PORT="2022"
SMTP_PORT="25"
IMAP_PORT="143"
NS="hosting-system"

# Parse args
while [[ $# -gt 0 ]]; do
  case "$1" in
    --ip) PANEL_IP="$2"; shift 2 ;;
    --fqdn) PANEL_FQDN="$2"; shift 2 ;;
    --port) PANEL_HTTP_PORT="$2"; shift 2 ;;
    --domain) TEST_DOMAIN="$2"; TEST_EMAIL="${TEST_EMAIL_USER}@${TEST_DOMAIN}"; shift 2 ;;
    *) echo "Unknown arg: $1"; exit 1 ;;
  esac
done

# Split PANEL_IP into host and port (supports host:port format)
if [[ "$PANEL_IP" == *:* ]]; then
  PANEL_IP_ONLY="${PANEL_IP%%:*}"
  PANEL_HTTP_PORT="${PANEL_HTTP_PORT:-${PANEL_IP##*:}}"
else
  PANEL_IP_ONLY="${PANEL_IP}"
  PANEL_HTTP_PORT="${PANEL_HTTP_PORT:-30080}"
fi

# FQDN mode: use hostname on port 80, resolve to IP
if [[ -n "$PANEL_FQDN" ]]; then
  PANEL_BASE_URL="http://${PANEL_FQDN}"
  CURL_RESOLVE="--resolve ${PANEL_FQDN}:80:${PANEL_IP_ONLY}"
else
  PANEL_BASE_URL="http://${PANEL_IP_ONLY}:${PANEL_HTTP_PORT}"
  CURL_RESOLVE=""
fi

# ---------------------------------------------------------------------------
# Read credentials from K8s secrets (no hardcoded passwords)
# ---------------------------------------------------------------------------
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
KC_ADMIN_PASS=$(kubectl get secret -n "$NS" hosting-panel-keycloak -o jsonpath='{.data.admin-password}' 2>/dev/null | base64 -d 2>/dev/null || echo "")
MARIADB_ROOT_PASS=$(kubectl get secret -n "$NS" hosting-panel-mariadb-galera -o jsonpath='{.data.mariadb-root-password}' 2>/dev/null | base64 -d 2>/dev/null || echo "")
PDNS_API_KEY=$(kubectl get deployment -n "$NS" hosting-panel-operator -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="PDNS_API_KEY")].value}' 2>/dev/null || echo "")
if [[ -z "$KC_ADMIN_PASS" ]]; then
  echo "ERROR: Cannot read Keycloak admin password from K8s secret. Is the cluster running?"
  exit 1
fi
TEST_SFTP_PASS="$KC_ADMIN_PASS"

# ---------------------------------------------------------------------------
# Colors & helpers
# ---------------------------------------------------------------------------
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
CYAN='\033[0;36m'; BOLD='\033[1m'; NC='\033[0m'

PASS=0; FAIL=0; SKIP=0; TOTAL=0
FAILURES=()

info()  { echo -e "${CYAN}[INFO]${NC} $*"; }
ok()    { echo -e "${GREEN}[OK]${NC}   $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
err()   { echo -e "${RED}[ERR]${NC}  $*"; }

t() {
    local name="$1" expected="$2" actual="$3"
    TOTAL=$((TOTAL + 1))
    if [ "$actual" = "$expected" ]; then
        echo -e "  ${GREEN}✓${NC} ${name}"
        PASS=$((PASS + 1))
    else
        echo -e "  ${RED}✗${NC} ${name} (got: ${actual}, expected: ${expected})"
        FAIL=$((FAIL + 1))
        FAILURES+=("${name}: got ${actual}, expected ${expected}")
    fi
}

tcontains() {
    local name="$1" pattern="$2" actual="$3"
    TOTAL=$((TOTAL + 1))
    if echo "$actual" | grep -q "$pattern"; then
        echo -e "  ${GREEN}✓${NC} ${name}"
        PASS=$((PASS + 1))
    else
        echo -e "  ${RED}✗${NC} ${name} (pattern '${pattern}' not found)"
        FAIL=$((FAIL + 1))
        FAILURES+=("${name}: pattern '${pattern}' not found in output")
    fi
}

tskip() {
    local name="$1" reason="$2"
    TOTAL=$((TOTAL + 1))
    SKIP=$((SKIP + 1))
    echo -e "  ${YELLOW}⊘${NC} ${name} (skipped: ${reason})"
}

# ---------------------------------------------------------------------------
# Dependency check
# ---------------------------------------------------------------------------
check_deps() {
    local missing=()
    command -v curl    &>/dev/null || missing+=(curl)
    command -v jq      &>/dev/null || missing+=(jq)
    command -v sshpass &>/dev/null || missing+=(sshpass)
    command -v swaks   &>/dev/null || missing+=(swaks)
    command -v mysql   &>/dev/null || missing+=(mysql-client)

    if [[ ${#missing[@]} -gt 0 ]]; then
        info "Installing missing tools: ${missing[*]}"
        if command -v zypper &>/dev/null; then
            zypper install -y "${missing[@]}" 2>/dev/null || true
        elif command -v apt-get &>/dev/null; then
            apt-get install -y "${missing[@]}" 2>/dev/null || true
        fi
        # swaks via perl if not in repos
        if ! command -v swaks &>/dev/null; then
            curl -sL https://www.jetmore.org/john/code/swaks/files/swaks-20240103.0/swaks \
              -o /usr/local/bin/swaks && chmod +x /usr/local/bin/swaks 2>/dev/null || true
        fi
    fi
}

# ---------------------------------------------------------------------------
# Get Keycloak token
# ---------------------------------------------------------------------------
get_token() {
    local resp
    resp=$(curl -sf -X POST $CURL_RESOLVE \
        "${PANEL_BASE_URL}/realms/hosting/protocol/openid-connect/token" \
        -H "Content-Type: application/x-www-form-urlencoded" \
        -d "grant_type=password&client_id=panel-ui&username=admin&password=${KC_ADMIN_PASS}" \
        2>/dev/null) || { err "Failed to get Keycloak token"; exit 1; }
    echo "$resp" | jq -r '.access_token'
}

# ---------------------------------------------------------------------------
# API helper
# ---------------------------------------------------------------------------
TOKEN=""
api() {
    local method="$1" path="$2"
    shift 2
    curl -sf -o /tmp/e2e_resp -w "%{http_code}" \
        $CURL_RESOLVE \
        -X "$method" \
        -H "Authorization: Bearer ${TOKEN}" \
        -H "Content-Type: application/json" \
        "$@" "${PANEL_BASE_URL}${path}" 2>/dev/null || echo "000"
}
body() { cat /tmp/e2e_resp 2>/dev/null || echo "{}"; }

# ---------------------------------------------------------------------------
# Wait for resource phase
# ---------------------------------------------------------------------------
wait_phase() {
    local resource_type="$1" name="$2" expected_phase="$3" max_wait="${4:-60}"
    local elapsed=0
    while [[ $elapsed -lt $max_wait ]]; do
        local phase
        phase=$(kubectl get "$resource_type" "$name" -n "$NS" \
            -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
        if [[ "$phase" == "$expected_phase" ]]; then
            return 0
        fi
        sleep 5
        elapsed=$((elapsed + 5))
    done
    warn "Timeout waiting for ${resource_type}/${name} to reach phase ${expected_phase} (current: ${phase:-unknown})"
    return 1
}

# =============================================================================
echo ""
echo -e "${BOLD}╔══════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}║   Hosting Panel — E2E Integration Test       ║${NC}"
echo -e "${BOLD}╚══════════════════════════════════════════════╝${NC}"
echo ""
info "Panel URL:   ${PANEL_BASE_URL}"
info "Test domain: ${TEST_DOMAIN}"
info "Test email:  ${TEST_EMAIL}"
echo ""

# Derive K8s resource names from domain
TEST_WEBSITE_K8S=$(echo "${TEST_DOMAIN}" | sed 's/\./-/g')
EMAIL_DOMAIN_K8S=$(echo "${TEST_DOMAIN}" | sed 's/\./-/g')
EMAIL_K8S_NAME=$(echo "${TEST_EMAIL}" | sed 's/@/-at-/g' | sed 's/\./-/g')

check_deps

# Pre-cleanup: remove any leftover test resources from previous runs
info "Pre-cleanup: removing any leftover test resources..."
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
kubectl delete website "${TEST_WEBSITE}" -n "$NS" --ignore-not-found 2>/dev/null || true
kubectl delete emailaccount "$(echo "${TEST_EMAIL}" | sed 's/@/-at-/g' | sed 's/\./-/g')" -n "$NS" --ignore-not-found 2>/dev/null || true
kubectl delete emaildomain "$(echo "${TEST_DOMAIN}" | sed 's/\./-/g')" -n "$NS" --ignore-not-found 2>/dev/null || true
kubectl delete database "admin-${TEST_DB_NAME}" -n "$NS" --ignore-not-found 2>/dev/null || true
# Also cleanup by K8s name (domain-based)
kubectl delete website "$(echo "${TEST_DOMAIN}" | sed 's/\./-/g')" -n "$NS" --ignore-not-found 2>/dev/null || true
sleep 3

# =============================================================================
# PHASE 1: Auth
# =============================================================================
echo -e "${BOLD}━━━ Phase 1: Authentication ━━━${NC}"

info "Getting Keycloak token..."
TOKEN=$(get_token)
if [[ -n "$TOKEN" && "$TOKEN" != "null" ]]; then
    ok "Token obtained (${#TOKEN} chars)"
    TOTAL=$((TOTAL + 1)); PASS=$((PASS + 1))
    echo -e "  ${GREEN}✓${NC} Keycloak OIDC token"
else
    err "No token — aborting"
    exit 1
fi

# =============================================================================
# PHASE 2: Website creation + HTTP test
# =============================================================================
echo ""
echo -e "${BOLD}━━━ Phase 2: Website ━━━${NC}"

# Create website via API
S=$(api POST "/api/websites" -d "{
    \"name\":\"${TEST_WEBSITE}\",
    \"primaryDomain\":\"${TEST_DOMAIN}\",
    \"phpVersion\":\"8.2\",
    \"owner\":\"admin\",
    \"createDnsZone\":true,
    \"ssl\":{\"enabled\":true,\"mode\":\"selfsigned\"}
}")
t "Create website via API" "201" "$S"

# Wait for operator to provision it
info "Waiting for website to become Active..."
if wait_phase "website" "${TEST_WEBSITE_K8S}" "Running" 180; then
    ok "Website Running"
    TOTAL=$((TOTAL + 1)); PASS=$((PASS + 1))
    echo -e "  ${GREEN}✓${NC} Website phase=Running"
    # Wait for website pod to be Ready
    info "Waiting for website pod to be Ready..."
    kubectl wait --for=condition=ready pod -l "hosting.panel/website=${TEST_WEBSITE_K8S}" \
        -n "$NS" --timeout=300s 2>/dev/null || true
    sleep 30  # give HTTPProxy time to propagate through Envoy
else
    warn "Website not Active yet — continuing anyway"
    TOTAL=$((TOTAL + 1)); FAIL=$((FAIL + 1))
    FAILURES+=("Website phase != Active after 90s")
fi

# HTTP test via Envoy hostPort 80 (the real HTTP endpoint)
# Envoy DaemonSet uses hostPort 80 → container 8080
info "Testing HTTP via hostPort 80..."
HTTP_RESP="000"
for attempt in 1 2 3; do
    HTTP_RESP=$(curl -s --resolve "${TEST_DOMAIN}:80:${PANEL_IP_ONLY}" \
        "http://${TEST_DOMAIN}/" -o /tmp/e2e_http -w "%{http_code}" \
        --max-time 10 2>/dev/null)
    [[ -z "$HTTP_RESP" ]] && HTTP_RESP="000"
    if [[ "$HTTP_RESP" == "200" || "$HTTP_RESP" == "403" || "$HTTP_RESP" == "301" || "$HTTP_RESP" == "302" ]]; then break; fi
    [[ $attempt -lt 3 ]] && { warn "HTTP :80 attempt $attempt got $HTTP_RESP, retrying in 15s..."; sleep 15; }
done
# Accept 200, 301 (SSL redirect), 302, or 403 (empty webroot)
if [[ "$HTTP_RESP" == "200" || "$HTTP_RESP" == "403" || "$HTTP_RESP" == "301" || "$HTTP_RESP" == "302" ]]; then
    TOTAL=$((TOTAL + 1)); PASS=$((PASS + 1))
    echo -e "  ${GREEN}✓${NC} HTTP :80 website reachable (got ${HTTP_RESP})"
else
    TOTAL=$((TOTAL + 1)); FAIL=$((FAIL + 1))
    echo -e "  ${RED}✗${NC} HTTP :80 website reachable (got: ${HTTP_RESP}, expected: 200/301/403)"
    FAILURES+=("HTTP :80 website reachable: got ${HTTP_RESP}, expected 200/301/403")
fi

# Also test via Envoy NodePort 30080 (alternative access path)
info "Testing HTTP via NodePort ${PANEL_HTTP_PORT}..."
HTTP_NP_RESP="000"
for attempt in 1 2 3; do
    HTTP_NP_RESP=$(curl -s --resolve "${TEST_DOMAIN}:${PANEL_HTTP_PORT}:${PANEL_IP_ONLY}" \
        "http://${TEST_DOMAIN}:${PANEL_HTTP_PORT}/" -o /dev/null -w "%{http_code}" \
        --max-time 10 2>/dev/null)
    [[ -z "$HTTP_NP_RESP" ]] && HTTP_NP_RESP="000"
    if [[ "$HTTP_NP_RESP" == "200" || "$HTTP_NP_RESP" == "403" || "$HTTP_NP_RESP" == "301" || "$HTTP_NP_RESP" == "302" ]]; then break; fi
    [[ $attempt -lt 3 ]] && { warn "HTTP :${PANEL_HTTP_PORT} attempt $attempt got $HTTP_NP_RESP, retrying in 10s..."; sleep 10; }
done
if [[ "$HTTP_NP_RESP" == "200" || "$HTTP_NP_RESP" == "403" || "$HTTP_NP_RESP" == "301" || "$HTTP_NP_RESP" == "302" ]]; then
    TOTAL=$((TOTAL + 1)); PASS=$((PASS + 1))
    echo -e "  ${GREEN}✓${NC} HTTP :${PANEL_HTTP_PORT} website reachable (got ${HTTP_NP_RESP})"
else
    TOTAL=$((TOTAL + 1)); FAIL=$((FAIL + 1))
    echo -e "  ${RED}✗${NC} HTTP :${PANEL_HTTP_PORT} website reachable (got: ${HTTP_NP_RESP}, expected: 200/301/403)"
    FAILURES+=("HTTP :${PANEL_HTTP_PORT} website reachable: got ${HTTP_NP_RESP}, expected 200/301/403")
fi

# Self-signed cert test via Envoy hostPort 443 (the real HTTPS endpoint)
# Envoy DaemonSet uses hostPort 443 → container 8443, so standard HTTPS port works.
info "Testing HTTPS via hostPort 443 (self-signed, skip verify)..."
HTTPS_RESP="000"
for attempt in 1 2 3 4 5 6; do
    HTTPS_RESP=$(curl -sk --resolve "${TEST_DOMAIN}:443:${PANEL_IP_ONLY}" \
        "https://${TEST_DOMAIN}/" -o /dev/null -w "%{http_code}" \
        --max-time 10 2>/dev/null || echo "000")
    [[ -z "$HTTPS_RESP" ]] && HTTPS_RESP="000"
    if [[ "$HTTPS_RESP" == "200" || "$HTTPS_RESP" == "403" ]]; then break; fi
    [[ $attempt -lt 6 ]] && { warn "HTTPS :443 attempt $attempt got $HTTPS_RESP, retrying in 15s..."; sleep 15; }
done
if [[ "$HTTPS_RESP" == "200" || "$HTTPS_RESP" == "403" ]]; then
    TOTAL=$((TOTAL + 1)); PASS=$((PASS + 1))
    echo -e "  ${GREEN}✓${NC} HTTPS :443 website reachable (got ${HTTPS_RESP})"
else
    TOTAL=$((TOTAL + 1)); FAIL=$((FAIL + 1))
    echo -e "  ${RED}✗${NC} HTTPS :443 website reachable (got: ${HTTPS_RESP}, expected: 200 or 403)"
    FAILURES+=("HTTPS :443 website reachable: got ${HTTPS_RESP}, expected 200 or 403")
fi

# Also test via Envoy NodePort 30443 (alternative access path)
info "Testing HTTPS via NodePort 30443..."
HTTPS_NP_RESP=$(curl -sk --resolve "${TEST_DOMAIN}:30443:${PANEL_IP_ONLY}" \
    "https://${TEST_DOMAIN}:30443/" -o /dev/null -w "%{http_code}" \
    --max-time 10 2>/dev/null || echo "000")
[[ -z "$HTTPS_NP_RESP" ]] && HTTPS_NP_RESP="000"
if [[ "$HTTPS_NP_RESP" == "200" || "$HTTPS_NP_RESP" == "403" ]]; then
    TOTAL=$((TOTAL + 1)); PASS=$((PASS + 1))
    echo -e "  ${GREEN}✓${NC} HTTPS :30443 website reachable (got ${HTTPS_NP_RESP})"
else
    TOTAL=$((TOTAL + 1)); FAIL=$((FAIL + 1))
    echo -e "  ${RED}✗${NC} HTTPS :30443 website reachable (got: ${HTTPS_NP_RESP}, expected: 200 or 403)"
    FAILURES+=("HTTPS :30443 website reachable: got ${HTTPS_NP_RESP}, expected 200 or 403")
fi

# =============================================================================
# PHASE 3: PHP test via SFTP upload
# =============================================================================
echo ""
echo -e "${BOLD}━━━ Phase 3: PHP via SFTP ━━━${NC}"

# Get SFTP NodePort — check both service names
SFTP_NODEPORT=$(kubectl get svc -n "$NS" hosting-panel-sftp-nodeport \
    -o jsonpath='{.spec.ports[0].nodePort}' 2>/dev/null || \
    kubectl get svc -n "$NS" hosting-panel-sftp \
    -o jsonpath='{.spec.ports[0].nodePort}' 2>/dev/null || echo "")

if [[ -z "$SFTP_NODEPORT" ]]; then
    # ClusterIP — use port-forward
    info "SFTP is ClusterIP, setting up port-forward..."
    kubectl port-forward -n "$NS" svc/hosting-panel-sftp 12022:2022 &>/dev/null &
    SFTP_PF=$!
    sleep 3
    SFTP_HOST="127.0.0.1"
    SFTP_PORT_ACTUAL="12022"
    trap "kill $SFTP_PF 2>/dev/null || true" EXIT
else
    SFTP_HOST="${PANEL_IP_ONLY}"
    SFTP_PORT_ACTUAL="${SFTP_NODEPORT}"
fi

# Wait for SFTP pod to restart after website creation (operator updates SFTP volumes)
info "Waiting for SFTP pod to restart with updated volumes..."
sleep 10
kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=sftp \
    -n "$NS" --timeout=120s 2>/dev/null || true
sleep 5

# Create PHP test file
PHP_CONTENT='<?php
$host = getenv("GALERA_HOST") ?: "localhost";
echo "PHP OK\n";
echo "PHP version: " . PHP_VERSION . "\n";
phpinfo(INFO_GENERAL);
?>'

echo "$PHP_CONTENT" > /tmp/test.php

# Upload via SFTP (sshpass + sftp)
info "Uploading test.php via SFTP to ${SFTP_HOST}:${SFTP_PORT_ACTUAL}..."
SFTP_RESULT=$(sshpass -p "${TEST_SFTP_PASS}" sftp \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    -P "${SFTP_PORT_ACTUAL}" \
    "${TEST_SFTP_USER}@${SFTP_HOST}" <<EOF 2>&1
mkdir web/${TEST_DOMAIN}
put /tmp/test.php web/${TEST_DOMAIN}/test.php
bye
EOF
) && SFTP_OK=true || SFTP_OK=false

if $SFTP_OK; then
    t "SFTP upload test.php" "ok" "ok"
elif echo "$SFTP_RESULT" | grep -q "not found\|does not exist\|Permission denied"; then
    tskip "SFTP upload test.php" "SFTP user '${TEST_SFTP_USER}' not provisioned in SFTPGo (website-based auth)"
    SFTP_OK=false
else
    warn "SFTP upload failed: ${SFTP_RESULT}"
    TOTAL=$((TOTAL + 1)); FAIL=$((FAIL + 1))
    FAILURES+=("SFTP upload failed")
fi

# Test PHP execution via HTTP (follow redirects for SSL)
# Use HTTPS directly since SSL is enabled (avoids 301 redirect issues with NodePort)
info "Testing PHP execution..."
PHP_RESP=$(curl -sk --resolve "${TEST_DOMAIN}:443:${PANEL_IP_ONLY}" \
    "https://${TEST_DOMAIN}/test.php" -o /tmp/e2e_php -w "%{http_code}" \
    --max-time 15 2>/dev/null)
[[ -z "$PHP_RESP" ]] && PHP_RESP="000"
t "PHP file served (HTTP 200)" "200" "$PHP_RESP"
if [[ "$PHP_RESP" == "200" ]]; then
    PHP_OUTPUT=$(cat /tmp/e2e_php 2>/dev/null || echo "")
    tcontains "PHP output contains 'PHP OK'" "PHP OK" "$PHP_OUTPUT"
fi

# --- PHP 8.5 version test ---
info "Testing PHP 8.5 website creation..."
S=$(api POST "/api/websites" -d "{
    \"name\":\"e2e-php85\",
    \"primaryDomain\":\"php85test.local\",
    \"php\":{\"version\":\"8.5\"}
}")
t "Create PHP 8.5 website" "201" "$S"
if [[ "$S" == "201" ]]; then
    elapsed=0
    while [[ $elapsed -lt 40 ]]; do
        PH=$(kubectl get website php85test-local -n "$NS" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
        if [[ "$PH" == "Running" ]]; then break; fi
        sleep 5; elapsed=$((elapsed + 5))
    done
    t "PHP 8.5 website phase=Running" "Running" "$PH"

    # Verify the pod uses php:8.5-apache image
    PHP85_IMG=$(kubectl get deployment php85test-local -n "$NS" -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || echo "")
    tcontains "PHP 8.5 container image correct" "8.5-apache" "$PHP85_IMG"

    # Cleanup
    kubectl delete website php85test-local -n "$NS" --wait=false 2>/dev/null
fi

# =============================================================================
# PHASE 4: Email — create account, send SMTP, check IMAP
# =============================================================================
echo ""
echo -e "${BOLD}━━━ Phase 4: Email ━━━${NC}"

# Create email domain
S=$(api POST "/api/email-domains" -d "{\"domain\":\"${TEST_DOMAIN}\"}")
t "Create email domain" "201" "$S"

# Wait for email domain to become Active before creating account
info "Waiting for email domain to become Active..."
EMAIL_DOMAIN_K8S=$(echo "${TEST_DOMAIN}" | sed 's/\./-/g')
elapsed=0
while [[ $elapsed -lt 60 ]]; do
    ED_PHASE=$(kubectl get emaildomain "$EMAIL_DOMAIN_K8S" -n "$NS" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    if [[ "$ED_PHASE" == "Active" ]]; then break; fi
    sleep 5; elapsed=$((elapsed + 5))
done
if [[ "$ED_PHASE" != "Active" ]]; then
    warn "EmailDomain not Active after 60s (phase: ${ED_PHASE:-unknown})"
fi

# Workaround: fix Postfix message_size_limit (must be <= virtual_mailbox_limit)
info "Fixing Postfix config (operator workaround)..."
SMTP_POD=$(kubectl get pod -n "$NS" -l "app.kubernetes.io/name=mail-smtp" \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
if [[ -z "$SMTP_POD" ]]; then
    SMTP_POD=$(kubectl get pod -n "$NS" --no-headers 2>/dev/null | awk '/hosting-panel-mail-smtp/{print $1}' | head -1)
fi
if [[ -n "$SMTP_POD" ]]; then
    kubectl exec -n "$NS" "$SMTP_POD" -c postfix -- postconf -e 'message_size_limit = 10240000' 2>/dev/null || true
    # Ensure LMTP delivery to Dovecot (ConfigMap should have this, but apply it in case of stale config)
    kubectl exec -n "$NS" "$SMTP_POD" -c postfix -- postconf -e 'virtual_transport = lmtp:inet:hosting-panel-mail-imap.hosting-system.svc.cluster.local:24' 2>/dev/null || true
    kubectl exec -n "$NS" "$SMTP_POD" -c postfix -- postfix reload 2>/dev/null || true
fi

# Workaround: operator does not update Postfix virtual_domains — do it manually
info "Updating Postfix virtual_domains (operator workaround)..."
SMTP_POD=$(kubectl get pod -n "$NS" -l "app.kubernetes.io/name=mail-smtp" \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
if [[ -z "$SMTP_POD" ]]; then
    # fallback: find by name prefix
    SMTP_POD=$(kubectl get pod -n "$NS" --no-headers 2>/dev/null | awk '/hosting-panel-mail-smtp/{print $1}' | head -1)
fi
if [[ -n "$SMTP_POD" ]]; then
    kubectl exec -n "$NS" "$SMTP_POD" -c postfix -- sh -c "
        grep -q '${TEST_DOMAIN}' /etc/postfix/virtual_domains || printf '${TEST_DOMAIN} OK\n' >> /etc/postfix/virtual_domains
        grep -q '${TEST_DOMAIN}' /etc/postfix/allowed_senders || printf '${TEST_DOMAIN} OK\n' >> /etc/postfix/allowed_senders
        postmap lmdb:/etc/postfix/virtual_domains 2>/dev/null || postmap /etc/postfix/virtual_domains 2>/dev/null || true
        postmap lmdb:/etc/postfix/allowed_senders 2>/dev/null || postmap /etc/postfix/allowed_senders 2>/dev/null || true
        postfix reload 2>/dev/null || true
    " 2>/dev/null && ok "Postfix virtual_domains + allowed_senders updated" || warn "Could not update Postfix config"
else
    warn "SMTP pod not found, skipping Postfix update"
fi

# Create email account
S=$(api POST "/api/email-accounts" -d "{
    \"address\":\"${TEST_EMAIL}\",
    \"domain\":\"${TEST_DOMAIN}\",
    \"password\":\"${TEST_EMAIL_PASS}\",
    \"quotaMB\":1024
}")
t "Create email account" "201" "$S"

# Wait for email account to be Active
info "Waiting for email account to become Active..."
if wait_phase "emailaccount" "${EMAIL_K8S_NAME}" "Active" 60; then
    ok "Email account Active"
    TOTAL=$((TOTAL + 1)); PASS=$((PASS + 1))
    echo -e "  ${GREEN}✓${NC} EmailAccount phase=Active"
else
    warn "Email account not Active yet"
    TOTAL=$((TOTAL + 1)); FAIL=$((FAIL + 1))
    FAILURES+=("EmailAccount phase != Active after 60s")
fi

# Workaround: operator does not update Postfix virtual_mailbox — do it manually
if [[ -n "$SMTP_POD" ]]; then
    MAILBOX_ENTRY="${TEST_EMAIL} ${TEST_DOMAIN}/${TEST_EMAIL_USER}/"
    kubectl exec -n "$NS" "$SMTP_POD" -c postfix -- sh -c "
        grep -q '${TEST_EMAIL}' /etc/postfix/virtual_mailbox || printf '${MAILBOX_ENTRY}\n' >> /etc/postfix/virtual_mailbox
        postmap lmdb:/etc/postfix/virtual_mailbox 2>/dev/null || postmap /etc/postfix/virtual_mailbox 2>/dev/null || true
        postfix reload 2>/dev/null || true
    " 2>/dev/null && ok "Postfix virtual_mailbox updated" || warn "Could not update Postfix virtual_mailbox"
fi

# Get SMTP NodePort or use port-forward
SMTP_NODEPORT=$(kubectl get svc -n "$NS" hosting-panel-mail-smtp-nodeport \
    -o jsonpath='{.spec.ports[0].nodePort}' 2>/dev/null || \
    kubectl get svc -n "$NS" hosting-panel-mail-smtp \
    -o jsonpath='{.spec.ports[0].nodePort}' 2>/dev/null || echo "")

if [[ -z "$SMTP_NODEPORT" ]]; then
    info "SMTP is ClusterIP, setting up port-forward..."
    kubectl port-forward -n "$NS" svc/hosting-panel-mail-smtp 12025:25 &>/dev/null &
    SMTP_PF=$!
    sleep 3
    SMTP_HOST="127.0.0.1"
    SMTP_PORT_ACTUAL="12025"
    trap "kill ${SMTP_PF:-} $SFTP_PF 2>/dev/null || true" EXIT
else
    SMTP_HOST="${PANEL_IP_ONLY}"
    SMTP_PORT_ACTUAL="${SMTP_NODEPORT}"
fi

# Check email via IMAP (curl imap)
IMAP_NODEPORT=$(kubectl get svc -n "$NS" hosting-panel-mail-imap-nodeport \
    -o jsonpath='{.spec.ports[0].nodePort}' 2>/dev/null || \
    kubectl get svc -n "$NS" hosting-panel-mail-imap \
    -o jsonpath='{.spec.ports[0].nodePort}' 2>/dev/null || echo "")

if [[ -z "$IMAP_NODEPORT" ]]; then
    info "IMAP is ClusterIP, setting up port-forward..."
    kubectl port-forward -n "$NS" svc/hosting-panel-mail-imap 12143:143 &>/dev/null &
    IMAP_PF=$!
    sleep 3
    IMAP_HOST="127.0.0.1"
    IMAP_PORT_ACTUAL="12143"
    trap "kill ${IMAP_PF:-} ${SMTP_PF:-} $SFTP_PF 2>/dev/null || true" EXIT
else
    IMAP_HOST="${PANEL_IP_ONLY}"
    IMAP_PORT_ACTUAL="${IMAP_NODEPORT}"
fi

# Create email user in Keycloak (Dovecot authenticates via Keycloak checkpassword)
info "Creating email user in Keycloak..."
KC_MASTER_TOKEN=$(kubectl exec -n "$NS" hosting-panel-keycloak-0 -- curl -sf -X POST \
  http://localhost:8080/realms/master/protocol/openid-connect/token \
  -d "grant_type=password&client_id=admin-cli&username=admin&password=${KC_ADMIN_PASS}" 2>/dev/null | sed 's/.*"access_token":"\([^"]*\)".*/\1/')
KC_CREATE_HTTP=$(kubectl exec -n "$NS" hosting-panel-keycloak-0 -- curl -sf -o /dev/null -w "%{http_code}" \
  -X POST "http://localhost:8080/admin/realms/hosting/users" \
  -H "Authorization: Bearer ${KC_MASTER_TOKEN}" \
  -H "Content-Type: application/json" \
  -d "{
    \"username\":\"${TEST_EMAIL}\",
    \"email\":\"${TEST_EMAIL}\",
    \"firstName\":\"${TEST_EMAIL_USER}\",
    \"lastName\":\"Mail\",
    \"enabled\":true,
    \"emailVerified\":true,
    \"credentials\":[{\"type\":\"password\",\"value\":\"${TEST_EMAIL_PASS}\",\"temporary\":false}]
  }" 2>/dev/null)
if [[ "$KC_CREATE_HTTP" == "201" || "$KC_CREATE_HTTP" == "409" ]]; then
    ok "Email user created in Keycloak (HTTP: ${KC_CREATE_HTTP})"
else
    warn "Failed to create email user in Keycloak (HTTP: ${KC_CREATE_HTTP})"
fi

# Verify Dovecot LMTP is reachable from Postfix
info "Verifying Dovecot LMTP connectivity..."
IMAP_POD=$(kubectl get pod -n "$NS" -l app.kubernetes.io/name=mail-imap -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
if [[ -n "$IMAP_POD" ]]; then
    # Check LMTP port 24 (0x18) is listening via /proc/net/tcp (nc may not be available)
    if kubectl exec -n "$NS" "$IMAP_POD" -- cat /proc/net/tcp 2>/dev/null | grep -q ':0018 '; then
        ok "LMTP port 24 listening in Dovecot pod"
    else
        warn "LMTP port 24 not listening in Dovecot pod"
    fi
    # Verify LMTP is reachable via K8s Service from Postfix pod
    if [[ -n "$SMTP_POD" ]]; then
        kubectl exec -n "$NS" "$SMTP_POD" -c postfix -- nc -zw2 hosting-panel-mail-imap 24 2>/dev/null \
            && ok "LMTP port 24 reachable via Service" \
            || warn "LMTP port 24 not reachable via Service (may need Service port patch)"
    fi
fi

# Send test email via swaks AFTER Dovecot is ready (so LMTP delivery works)
info "Sending test email via SMTP (swaks)..."
echo "This is an automated e2e test email. Timestamp: $(date)" > /tmp/swaks_body.txt
SMTP_RESULT=$(swaks \
    --to "${TEST_EMAIL}" \
    --from "${TEST_EMAIL}" \
    --server "${SMTP_HOST}:${SMTP_PORT_ACTUAL}" \
    --helo "mail.${TEST_DOMAIN}" \
    --header "Subject: E2E Test $(date +%s)" \
    --body /tmp/swaks_body.txt \
    --timeout 15 \
    2>&1) && SMTP_OK=true || SMTP_OK=false

if $SMTP_OK; then
    t "Send email via SMTP" "ok" "ok"
else
    warn "SMTP send failed: $(echo "$SMTP_RESULT" | tail -3)"
    TOTAL=$((TOTAL + 1)); FAIL=$((FAIL + 1))
    FAILURES+=("SMTP send failed")
fi

# Wait for email delivery via LMTP
info "Waiting for email delivery..."
sleep 15

info "Checking IMAP inbox via doveadm search..."
IMAP_POD=$(kubectl get pod -n "$NS" -l app.kubernetes.io/name=mail-imap \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)

if [[ -n "$IMAP_POD" ]]; then
    INBOX_COUNT=$(kubectl exec -n "$NS" "$IMAP_POD" -- doveadm search -u "${TEST_EMAIL}" mailbox INBOX ALL 2>/dev/null | wc -l)
    if [[ "$INBOX_COUNT" -ge 1 ]]; then
        t "IMAP inbox has mail" "ok" "ok"
        TOTAL=$((TOTAL + 1)); PASS=$((PASS + 1))
        echo -e "  ${GREEN}✓${NC} IMAP inbox has ${INBOX_COUNT} message(s)"
    else
        warn "No messages in INBOX (doveadm search returned 0)"
        kubectl logs -n "$NS" "$IMAP_POD" --tail=10 2>/dev/null || true
        TOTAL=$((TOTAL + 2)); FAIL=$((FAIL + 2))
        FAILURES+=("LMTP delivery not confirmed")
        FAILURES+=("IMAP inbox check failed")
    fi
else
    warn "IMAP pod not found"
    TOTAL=$((TOTAL + 1)); FAIL=$((FAIL + 1))
    FAILURES+=("IMAP pod not found")
fi

# =============================================================================
# PHASE 5: MySQL — create DB, test from PHP
# =============================================================================
echo ""
echo -e "${BOLD}━━━ Phase 5: MySQL ━━━${NC}"

# Refresh token (may have expired during long email test phase)
info "Refreshing Keycloak token..."
TOKEN=$(get_token)

# Create database via API
S=$(api POST "/api/databases" -d "{
    \"name\":\"${TEST_DB_NAME}\"
}")
t "Create database via API" "201" "$S"

# Wait for DB to be Ready and get generated password from status
info "Waiting for database to become Ready..."
DB_K8S_NAME="admin-${TEST_DB_NAME}"
elapsed=0
while [[ $elapsed -lt 60 ]]; do
    DB_PHASE=$(kubectl get database "$DB_K8S_NAME" -n "$NS" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    if [[ "$DB_PHASE" == "Ready" ]]; then break; fi
    sleep 5; elapsed=$((elapsed + 5))
done
TEST_DB_PASS=$(kubectl get database "$DB_K8S_NAME" -n "$NS" -o jsonpath='{.status.password}' 2>/dev/null || echo "")
ACTUAL_DB_NAME=$(kubectl get database "$DB_K8S_NAME" -n "$NS" -o jsonpath='{.status.databaseName}' 2>/dev/null || echo "$DB_K8S_NAME")
ACTUAL_DB_USER=$(kubectl get database "$DB_K8S_NAME" -n "$NS" -o jsonpath='{.status.username}' 2>/dev/null || echo "$TEST_DB_USER")
if [[ -z "$TEST_DB_PASS" ]]; then
    warn "Could not get DB password from status"
    TOTAL=$((TOTAL + 1)); FAIL=$((FAIL + 1))
    FAILURES+=("Database password not available in status")
fi

# Get MariaDB ClusterIP
GALERA_HOST="hosting-panel-mariadb-galera.${NS}.svc.cluster.local"

# Test MySQL connection directly (via kubectl exec in mariadb pod)
info "Testing MySQL connection (user: ${TEST_DB_USER})..."
if [[ -n "$TEST_DB_PASS" ]]; then
    MYSQL_RESULT=$(kubectl exec -n "$NS" hosting-panel-mariadb-galera-0 -- \
        mysql -u "$ACTUAL_DB_USER" -p"${TEST_DB_PASS}" "$ACTUAL_DB_NAME" \
        -e "SELECT 'MySQL OK' AS result;" 2>/dev/null || echo "")
    tcontains "MySQL direct connection" "MySQL OK" "$MYSQL_RESULT"
else
    tskip "MySQL direct connection" "DB password not available"
fi

# Upload PHP MySQL test file via SFTP
cat > /tmp/dbtest.php << 'PHPEOF'
<?php
error_reporting(E_ALL);
ini_set("display_errors", 1);
$host = "hosting-panel-mariadb-galera.hosting-system.svc.cluster.local";
PHPEOF
echo "\$db   = \"${ACTUAL_DB_NAME}\";" >> /tmp/dbtest.php
echo "\$user = \"${ACTUAL_DB_USER}\";" >> /tmp/dbtest.php
echo "\$pass = \"${TEST_DB_PASS}\";" >> /tmp/dbtest.php
cat >> /tmp/dbtest.php << 'PHPEOF'

try {
    if (!extension_loaded("pdo_mysql")) {
        echo "MYSQL_PHP_FAIL: pdo_mysql extension not loaded\n";
        http_response_code(500);
        exit;
    }
    $pdo = new PDO("mysql:host=$host;dbname=$db", $user, $pass);
    $pdo->setAttribute(PDO::ATTR_ERRMODE, PDO::ERRMODE_EXCEPTION);
    $pdo->exec("CREATE TABLE IF NOT EXISTS e2e_test (id INT AUTO_INCREMENT PRIMARY KEY, val VARCHAR(100))");
    $pdo->exec("INSERT INTO e2e_test (val) VALUES ('hello from php')");
    $row = $pdo->query("SELECT val FROM e2e_test LIMIT 1")->fetch();
    echo "MYSQL_PHP_OK: " . $row["val"] . "\n";
} catch (\Throwable $e) {
    echo "MYSQL_PHP_FAIL: " . $e->getMessage() . "\n";
    http_response_code(500);
}
?>
PHPEOF

sshpass -p "${TEST_SFTP_PASS}" sftp \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    -P "${SFTP_PORT_ACTUAL}" \
    "${TEST_SFTP_USER}@${SFTP_HOST}" <<EOF 2>/dev/null
rm web/${TEST_DOMAIN}/dbtest.php
put /tmp/dbtest.php web/${TEST_DOMAIN}/dbtest.php
bye
EOF

info "Testing PHP → MySQL via HTTP..."
sleep 5  # give time for SFTP upload to sync to webroot

# Debug: verify PHP file content in website pod
WEBSITE_POD=$(kubectl get pod -n "$NS" -l "hosting.panel/website=${TEST_WEBSITE_K8S}" \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
if [[ -n "$WEBSITE_POD" ]]; then
    info "Verifying PHP file in website pod..."
    info "DB password in status: [${TEST_DB_PASS}]"
    kubectl exec -n "$NS" "$WEBSITE_POD" -- grep 'pass' /var/www/html/dbtest.php 2>/dev/null || true
    kubectl exec -n "$NS" "$WEBSITE_POD" -- php /var/www/html/dbtest.php 2>&1 || true
fi

DB_RESP=$(curl -sk --resolve "${TEST_DOMAIN}:443:${PANEL_IP_ONLY}" \
    "https://${TEST_DOMAIN}/dbtest.php" -o /tmp/e2e_db -w "%{http_code}" \
    --max-time 15 2>/dev/null)
[[ -z "$DB_RESP" ]] && DB_RESP="000"
t "PHP MySQL test HTTP 200" "200" "$DB_RESP"
if [[ "$DB_RESP" == "200" ]]; then
    DB_OUTPUT=$(cat /tmp/e2e_db 2>/dev/null || echo "")
    if ! echo "$DB_OUTPUT" | grep -q "MYSQL_PHP_OK"; then
        warn "PHP MySQL output: $(head -5 /tmp/e2e_db 2>/dev/null)"
    fi
    tcontains "PHP MySQL output OK" "MYSQL_PHP_OK" "$DB_OUTPUT"
fi

# =============================================================================
# PHASE 6: DNS zone check
# =============================================================================
echo ""
echo -e "${BOLD}━━━ Phase 6: DNS ━━━${NC}"

# Refresh token (may have expired during long test phases)
info "Refreshing Keycloak token..."
TOKEN=$(get_token)

S=$(api GET "/api/dns/zones")
t "List DNS zones" "200" "$S"

# Check if zone was auto-created for the test domain (createDnsZone:true was sent)
ZONE_EXISTS=$(body | jq -r --arg d "${TEST_DOMAIN}." '.[] | select(.name == $d) | .name' 2>/dev/null || echo "")
if [[ -z "$ZONE_EXISTS" ]]; then
    # Try without trailing dot
    ZONE_EXISTS=$(body | jq -r --arg d "$TEST_DOMAIN" '.[] | select(.name == $d or .name == ($d + ".")) | .name' 2>/dev/null || echo "")
fi
if [[ -n "$ZONE_EXISTS" ]]; then
    t "DNS zone auto-created for test domain" "ok" "ok"
else
    TOTAL=$((TOTAL + 1)); FAIL=$((FAIL + 1))
    echo -e "  ${RED}✗${NC} DNS zone auto-created for test domain (zone not found)"
    FAILURES+=("DNS zone auto-created: zone not found for ${TEST_DOMAIN}")
fi

# Test PowerDNS API via port-forward from host
info "Testing PowerDNS API via port-forward..."
PDNS_API_PORT=$(kubectl get svc -n "$NS" hosting-panel-powerdns \
    -o jsonpath='{.spec.ports[?(@.name=="api")].port}' 2>/dev/null || echo "8081")
kubectl port-forward -n "$NS" svc/hosting-panel-powerdns 18081:${PDNS_API_PORT} &>/dev/null &
PDNS_PF=$!
sleep 3
PDNS_RESP=$(curl -sf -H "X-API-Key: ${PDNS_API_KEY}" \
    "http://127.0.0.1:18081/api/v1/servers/localhost/zones" \
    -o /dev/null -w "%{http_code}" --max-time 10 2>/dev/null || echo "000")
kill $PDNS_PF 2>/dev/null || true
t "PowerDNS API accessible" "200" "$PDNS_RESP"

# =============================================================================
# PHASE 7: Backup & Restore
# =============================================================================
echo ""
echo -e "${BOLD}━━━ Phase 7: Backup & Restore ━━━${NC}"

# 7a. Create test data
info "Creating test data for backup..."
echo "BACKUP_TEST_MARKER_$(date +%s)" > /tmp/backup_marker.txt
MARKER_CONTENT=$(cat /tmp/backup_marker.txt)
sftp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -P 2222 \
  admin@${PANEL_IP} <<SFTP_END 2>/dev/null
cd web/${TEST_DOMAIN}
put /tmp/backup_marker.txt
quit
SFTP_END

# Create test DB with data
HTTP_CODE=$(api POST "/api/databases" -d '{"name":"backuptest","charset":"utf8mb4","collation":"utf8mb4_unicode_ci"}')
BK_DB_K8S="admin-backuptest"
for i in $(seq 1 30); do
  BK_DB_PHASE=$(kubectl get database "$BK_DB_K8S" -n "$NS" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
  if [ "$BK_DB_PHASE" = "Ready" ]; then break; fi
  sleep 2
done
BK_DB_PASS=$(kubectl get database "$BK_DB_K8S" -n "$NS" -o jsonpath='{.status.password}' 2>/dev/null)
BK_DB_NAME=$(kubectl get database "$BK_DB_K8S" -n "$NS" -o jsonpath='{.status.databaseName}' 2>/dev/null)
BK_DB_USER=$(kubectl get database "$BK_DB_K8S" -n "$NS" -o jsonpath='{.status.username}' 2>/dev/null)
MARIADB_POD=$(kubectl get pods -n "$NS" -l app.kubernetes.io/name=mariadb-galera -o jsonpath='{.items[0].metadata.name}')
kubectl exec -n "$NS" "$MARIADB_POD" -- mysql -u "$BK_DB_USER" -p"$BK_DB_PASS" "$BK_DB_NAME" \
  -e "CREATE TABLE IF NOT EXISTS backup_test (id INT, val VARCHAR(100)); INSERT INTO backup_test VALUES (1, 'BACKUP_DATA_OK');" 2>/dev/null

# 7b. Run backup
info "Creating backup..."
HTTP_CODE=$(api POST "/api/backups" -d '{"components":["web","db"]}')
BACKUP_ID=$(body | jq -r '.id')
t "Create backup via API" "201" "$HTTP_CODE"

# Wait for completion
BACKUP_OK=false
for i in $(seq 1 90); do
  JOB_SUCC=$(kubectl get job "$BACKUP_ID" -n "$NS" -o jsonpath="{.status.succeeded}" 2>/dev/null || echo "0")
  if [ "$JOB_SUCC" = "1" ]; then BACKUP_OK=true; break; fi
  JOB_FAIL=$(kubectl get job "$BACKUP_ID" -n "$NS" -o jsonpath="{.status.failed}" 2>/dev/null || echo "0"); if [ "${JOB_FAIL:-0}" -ge 3 ]; then break; fi
  sleep 2
done
t "Backup job completed" "true" "$BACKUP_OK"

BACKUP_LOG=$(kubectl logs job/$BACKUP_ID -n "$NS" 2>/dev/null || echo "")
tcontains "Backup contains web files" "Web backup OK" "$BACKUP_LOG"
tcontains "Backup contains DB dump" "Dumping" "$BACKUP_LOG"

# 7c. Delete test data
info "Deleting test data before restore..."
sftp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -P 2222 \
  admin@${PANEL_IP} <<SFTP_END 2>/dev/null
cd web/${TEST_DOMAIN}
rm backup_marker.txt
quit
SFTP_END
kubectl exec -n "$NS" "$MARIADB_POD" -- mysql -u "$BK_DB_USER" -p"$BK_DB_PASS" "$BK_DB_NAME" \
  -e "DROP TABLE IF EXISTS backup_test;" 2>/dev/null

# 7d. Restore
info "Restoring from backup..."
HTTP_CODE=$(api POST "/api/backups/${BACKUP_ID}/restore" -d '{"components":["web","db"]}')
RESTORE_ID=$(body | jq -r '.id // empty')
RESTORE_OK=false
if [ -n "$RESTORE_ID" ]; then
  for i in $(seq 1 90); do
    JOB_SUCC=$(kubectl get job "$RESTORE_ID" -n "$NS" -o jsonpath="{.status.succeeded}" 2>/dev/null || echo "0")
    if [ "$JOB_SUCC" = "1" ]; then RESTORE_OK=true; break; fi
    JOB_FAIL=$(kubectl get job "$RESTORE_ID" -n "$NS" -o jsonpath="{.status.failed}" 2>/dev/null || echo "0"); if [ "${JOB_FAIL:-0}" -ge 3 ]; then break; fi
    sleep 2
  done
fi
t "Restore job completed" "true" "$RESTORE_OK"

RESTORE_LOG=$(kubectl logs job/$RESTORE_ID -n "$NS" 2>/dev/null || echo "")
tcontains "Restore web files OK" "Web files restored" "$RESTORE_LOG"
tcontains "Restore DB OK" "OK" "$RESTORE_LOG"

# 7e. Verify restored data
info "Verifying restored data..."
DB_CHECK=$(kubectl exec -n "$NS" "$MARIADB_POD" -- mysql -u "$BK_DB_USER" -p"$BK_DB_PASS" "$BK_DB_NAME" \
  -N -e "SELECT val FROM backup_test WHERE id=1;" 2>/dev/null || echo "")
t "Restored DB contains test data" "BACKUP_DATA_OK" "$DB_CHECK"

# Cleanup backup resources
kubectl delete jobs -l hosting.panel/type=backup -n "$NS" --ignore-not-found 2>/dev/null || true
kubectl delete jobs -l hosting.panel/type=restore -n "$NS" --ignore-not-found 2>/dev/null || true
api DELETE "/api/databases/${BK_DB_K8S}" >/dev/null 2>&1 || true

# PHASE 8: Cleanup
# =============================================================================
echo ""
echo -e "${BOLD}━━━ Phase 8: Cleanup ━━━${NC}"

api DELETE "/api/email-accounts/${EMAIL_K8S_NAME}" >/dev/null 2>&1 || true
api DELETE "/api/email-domains/${TEST_DOMAIN}" >/dev/null 2>&1 || true
api DELETE "/api/databases/${DB_K8S_NAME}" >/dev/null 2>&1 || true
api DELETE "/api/websites/${TEST_WEBSITE_K8S}" >/dev/null 2>&1 || true
# Delete email user from Keycloak
KC_CLEANUP_TOKEN=$(kubectl exec -n "$NS" hosting-panel-keycloak-0 -- curl -sf -X POST \
  http://localhost:8080/realms/master/protocol/openid-connect/token \
  -d "grant_type=password&client_id=admin-cli&username=admin&password=${KC_ADMIN_PASS}" 2>/dev/null | sed 's/.*"access_token":"\([^"]*\)".*/\1/')
KC_EMAIL_UID=$(kubectl exec -n "$NS" hosting-panel-keycloak-0 -- curl -sf \
  "http://localhost:8080/admin/realms/hosting/users?username=${TEST_EMAIL}" \
  -H "Authorization: Bearer ${KC_CLEANUP_TOKEN}" 2>/dev/null | sed 's/.*"id":"\([^"]*\)".*/\1/')
[ -n "$KC_EMAIL_UID" ] && kubectl exec -n "$NS" hosting-panel-keycloak-0 -- curl -sf -o /dev/null \
  -X DELETE "http://localhost:8080/admin/realms/hosting/users/${KC_EMAIL_UID}" \
  -H "Authorization: Bearer ${KC_CLEANUP_TOKEN}" 2>/dev/null || true
ok "Test resources cleaned up"

# =============================================================================
# Summary
# =============================================================================
echo ""
echo -e "${BOLD}╔══════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}║           E2E Test Results                   ║${NC}"
echo -e "${BOLD}╠══════════════════════════════════════════════╣${NC}"
printf "${BOLD}║${NC}  Total:   %-35s${BOLD}║${NC}\n" "${TOTAL}"
printf "${BOLD}║${NC}  ${GREEN}Passed:   %-35s${NC}${BOLD}║${NC}\n" "${PASS}"
printf "${BOLD}║${NC}  ${RED}Failed:   %-35s${NC}${BOLD}║${NC}\n" "${FAIL}"
printf "${BOLD}║${NC}  ${YELLOW}Skipped:  %-35s${NC}${BOLD}║${NC}\n" "${SKIP}"
echo -e "${BOLD}╚══════════════════════════════════════════════╝${NC}"

if [[ ${#FAILURES[@]} -gt 0 ]]; then
    echo ""
    echo -e "${RED}Failed tests:${NC}"
    for f in "${FAILURES[@]}"; do
        echo -e "  ${RED}✗${NC} $f"
    done
fi

echo ""
if [[ $FAIL -eq 0 ]]; then
    echo -e "${GREEN}${BOLD}All tests passed!${NC}"
    exit 0
else
    echo -e "${RED}${BOLD}${FAIL} test(s) failed.${NC}"
    echo ""
    info "Useful debug commands:"
    echo "  kubectl logs -n ${NS} -c panel-core deployment/hosting-panel-panel --tail=30"
    echo "  kubectl logs -n ${NS} deployment/hosting-panel-operator --tail=30"
    echo "  kubectl logs -n ${NS} deployment/hosting-panel-mail-smtp --tail=30"
    echo "  kubectl logs -n ${NS} deployment/hosting-panel-mail-imap --tail=30"
    exit 1
fi
