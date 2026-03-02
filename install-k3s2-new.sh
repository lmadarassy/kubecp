#!/bin/bash
# =============================================================================
# Hosting Panel — Clean Install Script for single-node k3s (SLES 15 SP7)
# Usage: bash install-k3s2.sh [--fqdn <FQDN>] [--ip <IP>] [--images-dir <path>] [--tag <tag>] [--operator-tag <tag>] [--sftp-tag <tag>] 
#
# Modes:
#   --fqdn <FQDN>  : (recommended) Use real DNS name. Subdomains: keycloak.FQDN, phpmyadmin.FQDN, webmail.FQDN
#                     Requires wildcard DNS (*.FQDN) pointing to the VM IP. Envoy listens on port 80/443 via hostPort.
#   --ip <IP>       : (fallback) IP-only mode. Services accessible via Envoy NodePort (30080/30443).
#                     Subdomains use IP suffix: keycloak.IP, phpmyadmin.IP (works only with /etc/hosts or nip.io).
#
# Prerequisites (manual, before running this script):
#   1. Import custom images:
#      k3s ctr images import /tmp/images/panel-core.tar
#      k3s ctr images import /tmp/images/panel-ui.tar
#      k3s ctr images import /tmp/images/hosting-operator.tar
#   2. This script must be run as root on the target VM
#   3. The helm-chart/ directory must be present at SCRIPT_DIR/helm-chart/
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# --- Defaults ---
VM_IP=""
PANEL_FQDN=""
IMAGES_DIR="/tmp/images"
IMAGE_TAG=""
OPERATOR_TAG=""
SFTP_TAG=""
UI_TAG=""
NS="hosting-system"

# --- Generate random passwords (override with env vars if needed) ---
gen_pw() { head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c "$1"; }
KEYCLOAK_ADMIN_PASS="${KEYCLOAK_ADMIN_PASS:-$(gen_pw 20)}"
MARIADB_ROOT_PASS="${MARIADB_ROOT_PASS:-$(gen_pw 20)}"
MARIADB_BACKUP_PASS="${MARIADB_BACKUP_PASS:-$(gen_pw 20)}"
MARIADB_HOSTING_PASS="${MARIADB_HOSTING_PASS:-$(gen_pw 20)}"
PDNS_API_KEY="${PDNS_API_KEY:-$(gen_pw 32)}"
SFTP_KC_SECRET="${SFTP_KC_SECRET:-$(gen_pw 32)}"
MAIL_KC_SECRET="${MAIL_KC_SECRET:-$(gen_pw 32)}"

# --- Parse args ---
while [[ $# -gt 0 ]]; do
  case "$1" in
    --ip) VM_IP="$2"; shift 2 ;;
    --fqdn) PANEL_FQDN="$2"; shift 2 ;;
    --images-dir) IMAGES_DIR="$2"; shift 2 ;;
    --tag) IMAGE_TAG="$2"; shift 2 ;;
    --operator-tag) OPERATOR_TAG="$2"; shift 2 ;;
    --sftp-tag) SFTP_TAG="$2"; shift 2 ;;
    --ui-tag) UI_TAG="$2"; shift 2 ;;
    *) echo "Unknown arg: $1"; exit 1 ;;
  esac
done

# --- Auto-detect IP ---
if [[ -z "$VM_IP" ]]; then
  VM_IP=$(ip addr show eth0 2>/dev/null | grep 'inet ' | awk '{print $2}' | cut -d/ -f1 | head -1)
  if [[ -z "$VM_IP" ]]; then
    echo "ERROR: Could not auto-detect IP. Use --ip <IP>"
    exit 1
  fi
fi

# --- Determine hostname (FQDN or IP) ---
if [[ -n "$PANEL_FQDN" ]]; then
  HOSTNAME="$PANEL_FQDN"
  USE_FQDN=true
else
  HOSTNAME="$VM_IP"
  USE_FQDN=false
fi

# --- Auto-detect image tag from tar filenames ---
if [[ -z "$IMAGE_TAG" ]]; then
  IMAGE_TAG=$(ls "${IMAGES_DIR}/panel-core"*.tar 2>/dev/null | sed 's/.*panel-core[_-]\?\(.*\)\.tar/\1/' | head -1)
  # Fallback: check imported images after k3s install
  if [[ -z "$IMAGE_TAG" ]]; then
    IMAGE_TAG="latest"
    echo "WARN: Could not detect tag from filenames, will auto-detect from imported images after k3s install"
  fi
fi

echo "============================================="
echo " Hosting Panel Install"
echo " VM IP:       $VM_IP"
echo " Hostname:    $HOSTNAME"
echo " Mode:        $(${USE_FQDN} && echo 'FQDN (port 80/443)' || echo 'IP-only (NodePort 30080/30443)')"
echo " Panel tag:   $IMAGE_TAG"
echo " Operator tag: ${OPERATOR_TAG:-$IMAGE_TAG}"
echo " SFTP tag:    ${SFTP_TAG:-$IMAGE_TAG}"
echo " UI tag:      ${UI_TAG:-$IMAGE_TAG}"
echo " Namespace:   $NS"
echo "============================================="

export KUBECONFIG=/etc/rancher/k3s/k3s.yaml

# =============================================================================
# STEP 0: DNS fix (SLES resets resolv.conf — set static DNS before anything)
# =============================================================================
echo ""
echo "=== [0/10] Fixing DNS ==="
echo "nameserver 150.132.95.20" > /etc/resolv.conf
# Make it persistent via netconfig
if command -v netconfig &>/dev/null; then
  sed -i 's/^NETCONFIG_DNS_STATIC_SERVERS=.*/NETCONFIG_DNS_STATIC_SERVERS="150.132.95.20"/' \
    /etc/sysconfig/network/config 2>/dev/null || true
  netconfig update -f 2>/dev/null || true
fi
echo "DNS: $(cat /etc/resolv.conf | grep nameserver)"

# =============================================================================
# STEP 1: SLES zypper repo-k + open-iscsi (Longhorn prerequisite)
# =============================================================================
echo ""
echo "=== [1/10] Adding SLES repos + Installing dependencies ==="

# Add SLES repos if not already present (needed for open-iscsi and other packages)
# NOTE: Set SLES_REPO_BASE env var to your SLES package mirror URL if needed.
# On Ubuntu/Debian, these are not needed — the script will use apt instead.
if [[ -n "${SLES_REPO_BASE:-}" ]] && command -v zypper &>/dev/null; then
  declare -A REPOS=(
    ["SLE-15-SP7-Module-Legacy"]="${SLES_REPO_BASE}/SLE-15-SP7-Module-Legacy"
    ["SLE-15-SP7-Module-Basesystem"]="${SLES_REPO_BASE}/SLE-15-SP7-Module-Basesystem"
    ["SLE-15-SP7-Module-Basesystem-Updates"]="${SLES_REPO_BASE}/SLE-15-SP7-Module-Basesystem-Updates"
    ["SLE-15-SP7-Module-Server-Applications"]="${SLES_REPO_BASE}/SLE-15-SP7-Module-Server-Applications"
    ["SLE-15-SP7-Module-Containers"]="${SLES_REPO_BASE}/SLE-15-SP7-Module-Containers/"
    ["SLE-15-SP7-Module-Containers-Updates"]="${SLES_REPO_BASE}/SLE-15-SP7-Module-Containers-Updates/"
    ["SLE-15-SP7-Module-Public-Cloud"]="${SLES_REPO_BASE}/SLE-15-SP7-Module-Public-Cloud"
    ["SLE-15-SP7-Module-Public-Cloud-Updates"]="${SLES_REPO_BASE}/SLE-15-SP7-Module-Public-Cloud-Updates"
  )
  for NAME in "${!REPOS[@]}"; do
    zypper lr "$NAME" &>/dev/null || zypper ar -C -f "${REPOS[$NAME]}" "$NAME" 2>/dev/null || true
  done
fi

# --- Longhorn prerequisites: open-iscsi, nfs-client, iscsi_tcp ---
if ! command -v iscsiadm &>/dev/null; then
  zypper install -y open-iscsi
fi
# nfs-client is required for Longhorn RWX volumes (NFS-based share-manager)
if ! command -v mount.nfs &>/dev/null; then
  echo "Installing nfs-client (required for Longhorn RWX volumes)..."
  zypper install -y nfs-client
fi
systemctl enable iscsid 2>/dev/null || true
systemctl start iscsid 2>/dev/null || true
echo "iscsiadm: $(iscsiadm -V 2>&1 || echo 'ok')"

# SLES VMs often ship with kernel-default-base which lacks iscsi_tcp module.
# Longhorn v1 data engine requires iscsi_tcp for volume attach.
if ! modprobe iscsi_tcp 2>/dev/null; then
  echo "WARN: iscsi_tcp module not available — installing kernel-default (full kernel)..."
  if rpm -q kernel-default-base &>/dev/null && ! rpm -q kernel-default &>/dev/null; then
    zypper install -y kernel-default
    NEED_REBOOT=true
    echo "kernel-default installed. REBOOT REQUIRED before Longhorn can attach volumes."
  else
    echo "ERROR: iscsi_tcp not available and kernel-default already installed or kernel-default-base not found."
    echo "Check: find /lib/modules/\$(uname -r) -name iscsi_tcp*"
  fi
else
  echo "iscsi_tcp module loaded"
fi
echo "iscsi_tcp" >> /etc/modules-load.d/iscsi.conf 2>/dev/null || true

# Abort if reboot is needed
if [[ "${NEED_REBOOT:-}" == "true" ]]; then
  echo ""
  echo "============================================="
  echo " REBOOT REQUIRED"
  echo " Run: reboot"
  echo " Then re-run: bash $0 $*"
  echo "============================================="
  exit 0
fi

# =============================================================================
# STEP 2: k3s
# =============================================================================
echo ""
echo "=== [2/10] Installing k3s ==="
if ! command -v k3s &>/dev/null; then
  curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC='--disable=traefik' sh -
else
  echo "k3s already installed: $(k3s --version | head -1)"
fi

# Set KUBECONFIG permanently
grep -q 'KUBECONFIG=/etc/rancher/k3s/k3s.yaml' /root/.bashrc 2>/dev/null || \
  echo 'export KUBECONFIG=/etc/rancher/k3s/k3s.yaml' >> /root/.bashrc
grep -q 'KUBECONFIG=/etc/rancher/k3s/k3s.yaml' /root/.profile 2>/dev/null || \
  echo 'export KUBECONFIG=/etc/rancher/k3s/k3s.yaml' >> /root/.profile

echo "Waiting for node to be Ready..."
for i in $(seq 1 30); do
  if kubectl get nodes 2>/dev/null | grep -q 'Ready'; then
    echo "Node is Ready"
    break
  fi
  sleep 5
done

# --- Import custom images (now that k3s is installed) ---
echo "Importing custom images from ${IMAGES_DIR}..."
for img in panel-core panel-ui hosting-operator hosting-panel-sftp; do
  TAR="${IMAGES_DIR}/${img}.tar"
  if [[ -f "$TAR" ]]; then
    echo "  Importing ${img}.tar..."
    k3s ctr images import "$TAR"
  else
    echo "  WARN: ${TAR} not found — skipping"
  fi
done

# Auto-detect panel-core tag from imported images if still "latest"
if [[ "$IMAGE_TAG" == "latest" ]]; then
  IMAGE_TAG=$(k3s ctr images list 2>/dev/null | grep 'hosting-panel/panel-core:' | head -1 | sed 's/.*panel-core:\([^ ]*\).*/\1/')
  if [[ -z "$IMAGE_TAG" ]]; then
    echo "ERROR: Could not detect panel-core image tag. Use --tag <tag>"
    exit 1
  fi
  echo "Auto-detected panel-core tag: ${IMAGE_TAG}"
fi

# Auto-detect operator tag from imported images if not specified
if [[ -z "$OPERATOR_TAG" ]]; then
  OPERATOR_TAG=$(k3s ctr images list 2>/dev/null | grep 'hosting-panel/hosting-operator:' | head -1 | sed 's/.*hosting-operator:\([^ ]*\).*/\1/')
  if [[ -z "$OPERATOR_TAG" ]]; then
    OPERATOR_TAG="$IMAGE_TAG"
    echo "WARN: Could not detect operator tag, using panel tag: ${OPERATOR_TAG}"
  else
    echo "Auto-detected operator tag: ${OPERATOR_TAG}"
  fi
fi

# Auto-detect SFTP tag from imported images if not specified
if [[ -z "$SFTP_TAG" ]]; then
  SFTP_TAG=$(k3s ctr images list 2>/dev/null | grep 'hosting-panel-sftp:' | head -1 | sed 's/.*hosting-panel-sftp:\([^ ]*\).*/\1/')
  if [[ -z "$SFTP_TAG" ]]; then
    SFTP_TAG="$IMAGE_TAG"
    echo "WARN: Could not detect SFTP tag, using panel tag: ${SFTP_TAG}"
  else
    echo "Auto-detected SFTP tag: ${SFTP_TAG}"
  fi
fi

# Auto-detect UI tag from imported images if not specified
if [[ -z "$UI_TAG" ]]; then
  UI_TAG=$(k3s ctr images list 2>/dev/null | grep 'hosting-panel/panel-ui:' | head -1 | sed 's/.*panel-ui:\([^ ]*\).*/\1/')
  if [[ -z "$UI_TAG" ]]; then
    UI_TAG="$IMAGE_TAG"
    echo "WARN: Could not detect UI tag, using panel tag: ${UI_TAG}"
  else
    echo "Auto-detected UI tag: ${UI_TAG}"
  fi
fi

# =============================================================================
# STEP 3: Helm
# =============================================================================
echo ""
echo "=== [3/10] Installing Helm ==="
if ! command -v helm &>/dev/null; then
  # Try download first, fallback message if fails
  if ! curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash; then
    echo "ERROR: Helm download failed. Copy helm binary from source host:"
    echo "  scp root@k3s:/usr/bin/helm root@$(hostname -I | awk '{print $1}'):/usr/local/bin/helm"
    exit 1
  fi
else
  echo "Helm already installed: $(helm version --short)"
fi

# =============================================================================
# STEP 4: Storage — Longhorn
# =============================================================================
echo ""
echo "=== [4/10] Storage: Installing Longhorn ==="

CHART_DIR="${SCRIPT_DIR}/helm-chart"

# open-iscsi must be running (installed in STEP 1)
modprobe iscsi_tcp 2>/dev/null && echo "iscsi_tcp loaded" || echo "ERROR: iscsi_tcp not available — Longhorn volumes will NOT attach."

LONGHORN_CHART="${CHART_DIR}/charts/longhorn-1.11.0.tgz"
if [[ ! -f "$LONGHORN_CHART" ]]; then
  echo "ERROR: Longhorn chart not found at ${LONGHORN_CHART}"
  echo "Copy from k3s: scp root@k3s:/root/hosting-panel/helm-chart/charts/longhorn-1.11.0.tgz ${CHART_DIR}/charts/"
  exit 1
fi

kubectl create namespace longhorn-system 2>/dev/null || true

if helm list -n longhorn-system 2>/dev/null | grep -q longhorn; then
  echo "Longhorn already installed"
else
  helm install longhorn "${LONGHORN_CHART}" \
    --namespace longhorn-system \
    --set defaultSettings.defaultReplicaCount=1 \
    --set defaultSettings.storageMinimalAvailablePercentage=5 \
    --set defaultSettings.storageOverProvisioningPercentage=200 \
    --set defaultSettings.storageReservedPercentageForDefaultDisk=10 \
    --set defaultSettings.guaranteedInstanceManagerCPU=0 \
    --set persistence.defaultClassReplicaCount=1 \
    --set longhornUI.replicas=0 \
    --set csi.attacherReplicaCount=1 \
    --set csi.provisionerReplicaCount=1 \
    --set csi.resizerReplicaCount=1 \
    --set csi.snapshotterReplicaCount=1 \
    --timeout 10m 2>&1 || echo "WARN: Longhorn install had issues"
fi

echo "Waiting for Longhorn manager to be Ready..."
for i in $(seq 1 24); do
  READY=$(kubectl get pods -n longhorn-system 2>/dev/null | grep 'longhorn-manager' | grep 'Running' | wc -l || true)
  if [[ "$READY" -ge 1 ]]; then
    echo "Longhorn manager is Ready"
    break
  fi
  echo "  waiting... ($i/24)"
  sleep 10
done

# Create kubecp StorageClass pointing to Longhorn
kubectl apply -f - <<'SCEOF'
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: kubecp
  annotations:
    storageclass.kubernetes.io/is-default-class: "false"
provisioner: driver.longhorn.io
allowVolumeExpansion: true
reclaimPolicy: Delete
parameters:
  numberOfReplicas: "1"
  staleReplicaTimeout: "30"
  fsType: ext4
SCEOF

echo "StorageClass kubecp:"
kubectl get storageclass kubecp 2>/dev/null || echo "WARN: kubecp StorageClass not found"

# STEP 5: Contour (HTTPProxy CRDs) — manifest install, no Helm (rate limit)
# =============================================================================
echo ""
echo "=== [5/10] Installing Contour (HTTPProxy CRDs) ==="
kubectl create namespace projectcontour 2>/dev/null || true
kubectl apply -f https://projectcontour.io/quickstart/contour.yaml

# Patch Envoy DaemonSet to use hostPort (single-IP mode: no LoadBalancer)
# hostPort maps node port 80->container 8080, 443->container 8443
echo "Patching Envoy to use hostPort..."
kubectl patch daemonset envoy -n projectcontour --type=json \
  -p='[
    {"op":"replace","path":"/spec/template/spec/containers/1/ports/0/containerPort","value":8080},
    {"op":"add","path":"/spec/template/spec/containers/1/ports/0/hostPort","value":80},
    {"op":"replace","path":"/spec/template/spec/containers/1/ports/1/containerPort","value":8443},
    {"op":"add","path":"/spec/template/spec/containers/1/ports/1/hostPort","value":443}
  ]' 2>/dev/null || echo "WARN: Envoy hostPort patch failed (may need manual fix)"

# Patch Envoy service to NodePort with specific ports (avoid conflicts with hosting NodePorts)
kubectl patch svc envoy -n projectcontour --type=json \
  -p='[{"op":"replace","path":"/spec/type","value":"NodePort"},{"op":"replace","path":"/spec/ports/0/nodePort","value":30080},{"op":"replace","path":"/spec/ports/1/nodePort","value":30443}]' \
  2>/dev/null || true

echo "Waiting for Contour..."
for i in $(seq 1 12); do
  READY=$(kubectl get pods -n projectcontour 2>/dev/null | grep '^contour' | awk '{print $2}' | grep -c '1/1' || true)
  if [[ "$READY" -ge 1 ]]; then
    echo "Contour is Running"
    break
  fi
  sleep 10
done

# =============================================================================
# STEP 6: cert-manager CRDs
# =============================================================================
echo ""
echo "=== [6/10] Installing cert-manager CRDs ==="
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.14.5/cert-manager.crds.yaml

# =============================================================================
# STEP 7: Namespace + dockerhub secret
# =============================================================================
echo ""
echo "=== [7/10] Creating namespace and dockerhub secret ==="
kubectl create namespace "$NS" 2>/dev/null || true

# Create dockerhub-creds secret from local Docker config (docker login was done on this host)
if [[ -f /root/.docker/config.json ]]; then
  echo "Creating dockerhub-creds from /root/.docker/config.json..."
  kubectl create secret generic dockerhub-creds \
    --from-file=.dockerconfigjson=/root/.docker/config.json \
    --type=kubernetes.io/dockerconfigjson \
    -n "$NS" \
    --dry-run=client -o yaml | kubectl apply -f -
  echo "dockerhub-creds secret created"
else
  echo "WARN: /root/.docker/config.json not found"
  echo "Run 'docker login' first, or create the secret manually:"
  echo "  kubectl create secret docker-registry dockerhub-creds -n $NS --docker-username=<user> --docker-password=<token>"
fi

# =============================================================================
# STEP 8: Helm chart — use pre-downloaded charts/ (no helm dep update, rate limit)
# =============================================================================
echo ""
echo "=== [8/10] Deploying Helm chart ==="

CHART_DIR="${SCRIPT_DIR}/helm-chart"

if [[ ! -f "${CHART_DIR}/charts/keycloak-25.2.0.tgz" ]]; then
  echo "ERROR: Helm sub-charts not found in ${CHART_DIR}/charts/"
  echo "Copy them from the source k3s host:"
  echo "  scp root@k3s:/root/hosting-panel/helm-chart/charts/*.tgz ${CHART_DIR}/charts/"
  exit 1
fi

# Generate values-install.yaml with the correct IP and image tag
cat > /tmp/values-install.yaml << VALEOF
global:
  hostname: "${HOSTNAME}"
  nodeCount: 1
  storageClass: "kubecp"
  namespace: "${NS}"
  networkMode: "single-ip"
  imagePullSecrets:
    - "dockerhub-creds"
  security:
    allowInsecureImages: true

externalIP: "${VM_IP}"

panel:
  replicas: 1
  image:
    tag: "${IMAGE_TAG}"
    pullPolicy: Never
  ui:
    image:
      tag: "${UI_TAG}"
      pullPolicy: Never
  service:
    type: ClusterIP
    port: 8080
  keycloakAdmin:
    enabled: true
    user: "admin"
    password: "${KEYCLOAK_ADMIN_PASS}"
    realm: "master"

operator:
  replicas: 1
  image:
    tag: "${OPERATOR_TAG}"
    pullPolicy: Never

longhorn:
  enabled: false
metallb:
  enabled: false
contour:
  enabled: false
certManager:
  enabled: false

keycloak:
  enabled: true
  auth:
    adminUser: "admin"
    adminPassword: "${KEYCLOAK_ADMIN_PASS}"
  replicaCount: 1
  image:
    registry: docker.io
    repository: bitnamilegacy/keycloak
  global:
    imagePullSecrets:
      - "dockerhub-creds"
  realm:
    name: "hosting"
    roles:
      - "admin"
      - "user"
    oidcClient:
      clientId: "panel-ui"
      redirectUris:
        - "http://${HOSTNAME}/*"
        - "https://${HOSTNAME}/*"
        - "http://${VM_IP}/*"
        - "https://${VM_IP}/*"
      webOrigins:
        - "http://${HOSTNAME}"
        - "https://${HOSTNAME}"
        - "http://${VM_IP}"
        - "https://${VM_IP}"
    ldapFederation:
      enabled: false
  postgresql:
    enabled: true
    image:
      registry: docker.io
      repository: bitnamilegacy/postgresql
    primary:
      persistence:
        storageClass: "kubecp"
        size: "1Gi"
  resources:
    requests:
      cpu: "250m"
      memory: "384Mi"
    limits:
      cpu: "750m"
      memory: "512Mi"
  extraEnvVars:
    - name: JAVA_OPTS
      value: "-XX:MaxRAMPercentage=50 -XX:MinRAMPercentage=50 -XX:InitialRAMPercentage=30 -XX:+UseG1GC -XX:+ExitOnOutOfMemoryError"
  externalDatabase:
    host: ""
  persistence:
    enabled: true
    storageClass: "kubecp"
    size: "1Gi"

mariadbGalera:
  enabled: true

mariadb-galera:
  replicaCount: 1
  image:
    registry: docker.io
    repository: bitnamilegacy/mariadb-galera
  global:
    imagePullSecrets:
      - "dockerhub-creds"
  rootUser:
    password: "${MARIADB_ROOT_PASS}"
  galera:
    mariabackup:
      password: "${MARIADB_BACKUP_PASS}"
    bootstrap:
      forceBootstrap: true
      bootstrapFromNode: 0
  persistence:
    enabled: true
    storageClass: "kubecp"
    size: "1Gi"
  db:
    name: "hosting"
    user: "hosting"
    password: "${MARIADB_HOSTING_PASS}"
  resources:
    requests:
      cpu: "100m"
      memory: "256Mi"
    limits:
      cpu: "1000m"
      memory: "1Gi"

phpmyadmin:
  enabled: true
  image:
    registry: docker.io
    repository: bitnamilegacy/phpmyadmin
  db:
    host: "hosting-panel-mariadb-galera.${NS}.svc.cluster.local"
    port: 3306

powerdns:
  enabled: true
  replicas: 1
  backend: "gsqlite3"
  api:
    key: "${PDNS_API_KEY}"
  service:
    type: ClusterIP
  storage:
    size: "1Gi"

sftp:
  enabled: true
  replicas: 1
  image:
    repository: hosting-panel-sftp
    tag: "${SFTP_TAG}"
    pullPolicy: Never
  service:
    type: ClusterIP
    port: 2022
  keycloakClientSecret: "${SFTP_KC_SECRET}"

mail:
  smtp:
    enabled: true
    replicas: 1
    service:
      type: ClusterIP
      port: 25
      submissionPort: 587
  imap:
    enabled: true
    replicas: 1
    service:
      type: ClusterIP
      imapPort: 143
      imapsPort: 993
      pop3Port: 110
      pop3sPort: 995
  keycloak:
    clientSecret: "${MAIL_KC_SECRET}"
  storage:
    size: "1Gi"

roundcube:
  enabled: true
  replicas: 1

backup:
  enabled: false

monitoring:
  enabled: false
clamav:
  enabled: false
rspamd:
  enabled: false
VALEOF

if helm list -n "$NS" 2>/dev/null | grep -q hosting-panel; then
  echo "Upgrading existing release..."
  helm upgrade hosting-panel "${CHART_DIR}" \
    --namespace "$NS" \
    -f /tmp/values-install.yaml \
    --timeout 15m 2>&1 || echo "WARN: helm upgrade had issues, check pod status"
else
  helm install hosting-panel "${CHART_DIR}" \
    --namespace "$NS" \
    -f /tmp/values-install.yaml \
    --timeout 15m 2>&1 || echo "WARN: helm install had issues, check pod status"
fi

# =============================================================================
# STEP 9: CRDs + RBAC
# =============================================================================
echo ""
echo "=== [9/10] Applying CRDs ==="
# Helm upgrade does not update CRDs in crds/ directory — apply them manually
kubectl apply -f "${CHART_DIR}/crds/" 2>/dev/null || true

# =============================================================================
# STEP 10: Keycloak realm setup
# =============================================================================
echo ""
echo "=== [10/10] Keycloak realm setup ==="
echo "The keycloak-setup Job runs automatically as a Helm post-install hook."
echo "Waiting for Keycloak pod to be ready..."
for i in $(seq 1 36); do
  READY=$(kubectl get pods -n "$NS" 2>/dev/null | grep 'keycloak-0' | grep '1/1' | wc -l || true)
  if [[ "$READY" -ge 1 ]]; then
    echo "Keycloak is Ready"
    break
  fi
  echo "  waiting... ($i/36)"
  sleep 10
done

# Check if the setup job completed
SETUP_STATUS=$(kubectl get job -n "$NS" -l app.kubernetes.io/name=keycloak-setup -o jsonpath='{.items[0].status.succeeded}' 2>/dev/null)
if [[ "$SETUP_STATUS" == "1" ]]; then
  echo "Keycloak setup Job completed successfully"
else
  echo "WARN: Keycloak setup Job not yet completed. Check: kubectl logs -n $NS -l app.kubernetes.io/name=keycloak-setup"
fi

# =============================================================================
# Create default "Unlimited" hosting plan
# =============================================================================
echo ""
echo "=== Creating default Unlimited hosting plan ==="
kubectl apply -f - <<PLAN_EOF
apiVersion: hosting.hosting.panel/v1alpha1
kind: HostingPlan
metadata:
  name: unlimited
  namespace: $NS
spec:
  displayName: "Unlimited"
  default: true
  limits:
    websites: 0
    databases: 0
    emailAccounts: 0
    storageGB: 0
    cpuMillicores: 0
    memoryMB: 0
PLAN_EOF

# =============================================================================
# Workaround: SFTP volume mount (operator SyncSFTPVolumes bug)
# =============================================================================
echo ""
echo "=== Workaround: SFTP uv-admin volume mount ==="
# The operator should mount user PVCs into the SFTP deployment, but SyncSFTPVolumes
# doesn't execute in the current operator image. Patch manually.
SFTP_HAS_VOL=$(kubectl get deployment hosting-panel-sftp -n "$NS" -o jsonpath='{.spec.template.spec.volumes}' 2>/dev/null)
if [[ -z "$SFTP_HAS_VOL" || "$SFTP_HAS_VOL" == "null" ]]; then
  kubectl patch deployment hosting-panel-sftp -n "$NS" --type=json \
    -p='[{"op":"add","path":"/spec/template/spec/volumes","value":[{"name":"uv-admin","persistentVolumeClaim":{"claimName":"uv-admin"}}]},{"op":"add","path":"/spec/template/spec/containers/0/volumeMounts","value":[{"name":"uv-admin","mountPath":"/home/admin"}]}]' \
    2>/dev/null && echo "SFTP patched with uv-admin volume" || echo "WARN: SFTP patch failed"
else
  echo "SFTP already has volumes, skipping patch"
fi

# =============================================================================
# Summary
# =============================================================================
echo ""
echo "============================================="
echo " Install complete!"
echo "============================================="
kubectl get pods -n "$NS"
echo ""
if ${USE_FQDN}; then
  echo " Panel UI:    http://${HOSTNAME}/"
  echo " phpMyAdmin:  http://phpmyadmin.${HOSTNAME}/"
  echo " Webmail:     http://webmail.${HOSTNAME}/"
  echo " Keycloak:    http://keycloak.${HOSTNAME}/"
else
  ENVOY_HTTP=$(kubectl get svc -n projectcontour 2>/dev/null | grep envoy | grep -oP '\d+:(\d+)/TCP' | grep ':80' | grep -oP ':\K\d+' | head -1)
  if [[ -n "$ENVOY_HTTP" ]]; then
    echo " Panel UI: http://${VM_IP}:${ENVOY_HTTP}/"
  else
    echo " Get Envoy NodePort: kubectl get svc -n projectcontour"
  fi
fi
echo ""
echo "============================================="
echo " Generated Credentials (SAVE THESE!)"
echo "============================================="
echo " Keycloak admin:       admin / ${KEYCLOAK_ADMIN_PASS}"
echo " MariaDB root:         root / ${MARIADB_ROOT_PASS}"
echo " MariaDB backup:       mariabackup / ${MARIADB_BACKUP_PASS}"
echo " MariaDB hosting user: hosting / ${MARIADB_HOSTING_PASS}"
echo " PowerDNS API key:     ${PDNS_API_KEY}"
echo " SFTP KC secret:       ${SFTP_KC_SECRET}"
echo " Mail KC secret:       ${MAIL_KC_SECRET}"
echo "============================================="
