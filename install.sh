#!/bin/bash
# =============================================================================
# KubeCP — Single-node install script
# Installs a complete hosting panel on a fresh Linux host with k3s.
# All container images are pulled from GHCR (no dockerhub dependency).
#
# Usage: bash install.sh --fqdn <FQDN> --ip <IP> --dns <DNS_SERVER>
# Example: bash install.sh --fqdn panel.example.com --ip 203.0.113.10 --dns 8.8.8.8
# =============================================================================
set -euo pipefail

FQDN="" IP="" DNS=""
NS="hosting-system"
GHCR="ghcr.io/lmadarassy/kubecp"

while [[ $# -gt 0 ]]; do
  case $1 in
    --fqdn) FQDN="$2"; shift 2 ;;
    --ip)   IP="$2"; shift 2 ;;
    --dns)  DNS="$2"; shift 2 ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

[[ -z "$FQDN" || -z "$IP" ]] && { echo "Usage: $0 --fqdn <FQDN> --ip <IP> --dns <DNS_SERVER>"; exit 1; }

info() { echo -e "\033[0;36m[INFO]\033[0m $*"; }
ok()   { echo -e "\033[0;32m[ OK]\033[0m $*"; }
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Generate passwords
gen_pass() { head -c 32 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9' | head -c "${1:-16}"; }
MARIADB_ROOT_PASS=$(gen_pass 20)
MARIADB_BACKUP_PASS=$(gen_pass 20)
PDNS_API_KEY=$(gen_pass 24)

# ── 1. DNS ───────────────────────────────────────────────────────────────────
if [[ -n "$DNS" ]]; then
  info "Configuring DNS (${DNS})..."
  # Prevent NetworkManager/netconfig from overwriting resolv.conf
  sed -i 's/^NETCONFIG_DNS_POLICY=.*/NETCONFIG_DNS_POLICY=""/' /etc/sysconfig/network/config 2>/dev/null || true
  echo "nameserver ${DNS}" > /etc/resolv.conf
fi
curl -s -o /dev/null -w "%{http_code}" https://ghcr.io/v2/ --max-time 10 2>/dev/null | grep -qE "401|200" || { echo "ERROR: Cannot reach ghcr.io. Set --dns or check network."; exit 1; }
ok "Internet reachable"

# ── 2. Swap (if < 6GB RAM) ──────────────────────────────────────────────────
TOTAL_RAM=$(awk '/MemTotal/{print int($2/1024)}' /proc/meminfo)
if [[ $TOTAL_RAM -lt 6144 ]] && ! swapon --show | grep -q /swapfile; then
  info "Creating 2GB swap (${TOTAL_RAM}MB RAM detected)..."
  dd if=/dev/zero of=/swapfile bs=1M count=2048 2>/dev/null
  chmod 600 /swapfile && mkswap /swapfile >/dev/null && swapon /swapfile
  grep -q swapfile /etc/fstab || echo "/swapfile none swap sw 0 0" >> /etc/fstab
fi

# ── 3. k3s ───────────────────────────────────────────────────────────────────
info "Installing k3s..."
mkdir -p /etc/rancher/k3s
cat > /etc/rancher/k3s/config.yaml <<EOF
kubelet-arg:
  - "eviction-hard=imagefs.available<5%,nodefs.available<5%"
  - "eviction-soft=imagefs.available<10%,nodefs.available<10%"
  - "eviction-soft-grace-period=imagefs.available=1m,nodefs.available=1m"
EOF
curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="--disable=traefik --disable=servicelb" sh -
for i in $(seq 1 30); do /usr/local/bin/kubectl get nodes &>/dev/null && break; sleep 2; done
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
export PATH="/usr/local/bin:$PATH"
ok "k3s ready ($(kubectl get nodes -o jsonpath='{.items[0].status.nodeInfo.kubeletVersion}'))"

# ── 4. Contour ingress + cert-manager ────────────────────────────────────────
info "Installing Contour + cert-manager..."
kubectl apply -f https://projectcontour.io/quickstart/contour.yaml 2>&1 | tail -3
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.14.4/cert-manager.yaml 2>&1 | tail -3

info "Configuring Envoy with hostNetwork..."
sleep 20
kubectl get ds envoy -n projectcontour -o yaml > /tmp/envoy-ds.yaml
python3 -c "
import yaml
with open('/tmp/envoy-ds.yaml') as f: ds = yaml.safe_load(f)
s = ds['spec']['template']['spec']
s['hostNetwork'] = True; s['dnsPolicy'] = 'ClusterFirstWithHostNet'
for c in s['containers']:
    if c['name'] == 'envoy':
        c['ports'] = [{'name':'http','containerPort':8080,'protocol':'TCP'},{'name':'https','containerPort':8443,'protocol':'TCP'},{'name':'metrics','containerPort':8002,'protocol':'TCP'}]
for k in ['resourceVersion','uid','creationTimestamp','generation']: ds['metadata'].pop(k,None)
ds['metadata'].get('annotations',{}).pop('kubectl.kubernetes.io/last-applied-configuration',None)
ds.pop('status',None)
with open('/tmp/envoy-fixed.yaml','w') as f: yaml.dump(ds,f,default_flow_style=False)
"
kubectl delete ds envoy -n projectcontour 2>/dev/null
kubectl apply -f /tmp/envoy-fixed.yaml 2>&1 | tail -1
# Forward standard ports to Envoy (so user websites work on 80/443)
if command -v nft &>/dev/null; then
  nft add table ip nat 2>/dev/null || true
  nft add chain ip nat prerouting '{ type nat hook prerouting priority -100; }' 2>/dev/null || true
  nft add rule ip nat prerouting tcp dport 80 redirect to :8080 2>/dev/null || true
  nft add rule ip nat prerouting tcp dport 443 redirect to :8443 2>/dev/null || true
elif command -v iptables &>/dev/null; then
  iptables -t nat -A PREROUTING -p tcp --dport 80 -j REDIRECT --to-port 8080 2>/dev/null || true
  iptables -t nat -A PREROUTING -p tcp --dport 443 -j REDIRECT --to-port 8443 2>/dev/null || true
fi
ok "Contour ready (admin: 8080/8443, websites: 80/443)"

# ── 5. Helm install ──────────────────────────────────────────────────────────
kubectl create namespace $NS 2>/dev/null || true
mkdir -p /data/kubecp && chmod 777 /data/kubecp
ARCH=$(uname -m | sed "s/x86_64/amd64/;s/aarch64/arm64/")
command -v helm &>/dev/null || { curl -sfL "https://get.helm.sh/helm-v3.17.3-linux-${ARCH}.tar.gz" | tar xz -C /tmp; cp /tmp/linux-${ARCH}/helm /usr/local/bin/; }

CHART_REF="oci://ghcr.io/lmadarassy/kubecp/charts/hosting-panel"
# Use local chart if available, otherwise pull from GHCR
if [[ -d "${SCRIPT_DIR}/helm-chart" ]]; then
  CHART_REF="${SCRIPT_DIR}/helm-chart"
elif [[ -f "${SCRIPT_DIR}/helm-chart.tar.gz" ]]; then
  cd /tmp; tar xzf "${SCRIPT_DIR}/helm-chart.tar.gz"; CHART_REF="/tmp/helm-chart"
fi

# Build chart dependencies if using local chart directory
if [[ -d "$CHART_REF" ]]; then
  info "Building Helm chart dependencies..."
  helm repo add bitnami https://charts.bitnami.com/bitnami 2>/dev/null || true
  helm repo add jetstack https://charts.jetstack.io 2>/dev/null || true
  helm repo add longhorn https://charts.longhorn.io 2>/dev/null || true
  helm dependency build "$CHART_REF" 2>&1 | tail -3
fi

info "Installing hosting-panel via Helm..."
helm install hosting-panel "$CHART_REF" \
  --namespace $NS --timeout 2m --no-hooks \
  --set global.hostname="${FQDN}" --set global.externalIP="${IP}" \
  --set global.security.allowInsecureImages=true \
  --set panel.image.repository="${GHCR}/panel-core" --set panel.image.tag="latest" \
  --set panel.ui.image.repository="${GHCR}/panel-ui" --set panel.ui.image.tag="latest" \
  --set operator.image.repository="${GHCR}/hosting-operator" --set operator.image.tag="latest" \
  --set sftp.image.repository="${GHCR}/sftp" --set sftp.image.tag="latest" \
  --set backup.image.repository="${GHCR}/panel-backup" --set backup.image.tag="latest" \
  --set mariadb-galera.image.registry=ghcr.io --set mariadb-galera.image.repository=lmadarassy/kubecp/mirror/mariadb-galera --set mariadb-galera.image.tag=12.0.2 \
  --set mariadb-galera.rootUser.password="${MARIADB_ROOT_PASS}" --set mariadb-galera.galera.mariabackup.password="${MARIADB_BACKUP_PASS}" \
  --set keycloak.image.registry=ghcr.io --set keycloak.image.repository=lmadarassy/kubecp/mirror/keycloak --set keycloak.image.tag=26.3.3 \
  --set keycloak.postgresql.image.registry=ghcr.io --set keycloak.postgresql.image.repository=lmadarassy/kubecp/mirror/postgresql --set keycloak.postgresql.image.tag=latest \
  --set powerdns.api.key="${PDNS_API_KEY}" \
  --set metallb.enabled=false --set monitoring.enabled=false --set contour.enabled=false \
  --set longhorn.enabled=false --set certManager.enabled=false \
  --set phpmyadmin.enabled=false --set clamav.enabled=false \
  2>&1 | tail -3
kubectl scale statefulset hosting-panel-mariadb-galera -n $NS --replicas=1 2>/dev/null || true
ok "Helm install done"

# ── 6. Wait for MariaDB → init databases ─────────────────────────────────────
info "Waiting for MariaDB..."
kubectl wait --for=condition=ready pod/hosting-panel-mariadb-galera-0 -n $NS --timeout=300s 2>/dev/null || true
# Extra wait for mysql to accept connections
set +e
for i in $(seq 1 30); do
  kubectl exec hosting-panel-mariadb-galera-0 -n $NS -- mysql -uroot -p"${MARIADB_ROOT_PASS}" -e "SELECT 1" &>/dev/null && break
  info "  MariaDB not ready yet, retrying ($i/30)..."
  sleep 10
done
set -e

PDNS_DB_PASS=$(kubectl get secret hosting-panel-powerdns-db -n $NS -o jsonpath='{.data.password}' | base64 -d)

info "Creating databases + PowerDNS schema..."
kubectl exec hosting-panel-mariadb-galera-0 -n $NS -- mysql -uroot -p"${MARIADB_ROOT_PASS}" -e "
  CREATE DATABASE IF NOT EXISTS powerdns; CREATE DATABASE IF NOT EXISTS hosting;
  CREATE USER IF NOT EXISTS 'powerdns'@'%' IDENTIFIED BY '${PDNS_DB_PASS}';
  ALTER USER 'powerdns'@'%' IDENTIFIED BY '${PDNS_DB_PASS}';
  GRANT ALL ON powerdns.* TO 'powerdns'@'%'; FLUSH PRIVILEGES;" 2>&1 | tail -1

for tbl in \
  "domains (id INT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(255) NOT NULL, master VARCHAR(128), last_check INT, type VARCHAR(8) NOT NULL, notified_serial INT UNSIGNED, account VARCHAR(40), options TEXT, catalog VARCHAR(255), UNIQUE KEY name_index (name))" \
  "records (id BIGINT AUTO_INCREMENT PRIMARY KEY, domain_id INT, name VARCHAR(255), type VARCHAR(10), content TEXT, ttl INT, prio INT, disabled TINYINT(1) DEFAULT 0, ordername VARCHAR(255) BINARY, auth TINYINT(1) DEFAULT 1, KEY nametype_index (name,type), KEY domain_id (domain_id), CONSTRAINT records_ibfk_1 FOREIGN KEY (domain_id) REFERENCES domains (id) ON DELETE CASCADE)" \
  "supermasters (ip VARCHAR(64) NOT NULL, nameserver VARCHAR(255) NOT NULL, account VARCHAR(40) NOT NULL, PRIMARY KEY (ip, nameserver))" \
  "comments (id INT AUTO_INCREMENT PRIMARY KEY, domain_id INT NOT NULL, name VARCHAR(255) NOT NULL, type VARCHAR(10) NOT NULL, modified_at INT NOT NULL, account VARCHAR(40) NOT NULL, comment TEXT NOT NULL, KEY comments_name_type_idx (name, type), KEY comments_order_idx (domain_id, modified_at), CONSTRAINT comments_ibfk_1 FOREIGN KEY (domain_id) REFERENCES domains (id) ON DELETE CASCADE)" \
  "domainmetadata (id INT AUTO_INCREMENT PRIMARY KEY, domain_id INT NOT NULL, kind VARCHAR(32), content TEXT, KEY domainmetadata_idx (domain_id, kind), CONSTRAINT domainmetadata_ibfk_1 FOREIGN KEY (domain_id) REFERENCES domains (id) ON DELETE CASCADE)" \
  "cryptokeys (id INT AUTO_INCREMENT PRIMARY KEY, domain_id INT NOT NULL, flags INT NOT NULL, active BOOL, published BOOL DEFAULT 1, content TEXT, KEY domainidindex (domain_id), CONSTRAINT cryptokeys_ibfk_1 FOREIGN KEY (domain_id) REFERENCES domains (id) ON DELETE CASCADE)" \
  "tsigkeys (id INT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(255), algorithm VARCHAR(50), secret VARCHAR(255), UNIQUE KEY namealgoindex (name, algorithm))"; do
  kubectl exec hosting-panel-mariadb-galera-0 -n $NS -- mysql -uroot -p"${MARIADB_ROOT_PASS}" powerdns -e "CREATE TABLE IF NOT EXISTS $tbl Engine=InnoDB;" 2>/dev/null
done
kubectl delete pod hosting-panel-powerdns-0 -n $NS --force --grace-period=0 2>/dev/null || true
ok "Databases initialized"

# ── 7. Wait for Keycloak → configure realm ────────────────────────────────────
info "Waiting for Keycloak (takes ~3 min on first start)..."
set +e
for i in $(seq 1 60); do
  sleep 5
  READY=$(kubectl get pod hosting-panel-keycloak-0 -n $NS -o jsonpath='{.status.containerStatuses[0].ready}' 2>/dev/null)
  [ "$READY" = "true" ] && break
done
set -e

KC_PASS=$(kubectl get secret hosting-panel-keycloak -n $NS -o jsonpath='{.data.admin-password}' | base64 -d)
SFTP_SECRET=$(kubectl get secret hosting-panel-sftp-client -n $NS -o jsonpath='{.data.secret}' | base64 -d)
MAIL_SECRET=$(gen_pass 32)

info "Configuring Keycloak realm..."
cat > /tmp/kc-setup.sh <<'ENDSCRIPT'
#!/bin/sh
set -e
KC="http://hosting-panel-keycloak.hosting-system.svc.cluster.local"
TOKEN=$(curl -sf ${KC}/realms/master/protocol/openid-connect/token -d "username=admin&password=__KC_PASS__&grant_type=password&client_id=admin-cli" | sed 's/.*access_token.."\([^"]*\)".*/\1/')
[ -z "$TOKEN" ] && exit 1
H="Authorization: Bearer ${TOKEN}"
curl -sf -o /dev/null -X POST ${KC}/admin/realms -H "$H" -H "Content-Type: application/json" -d '{"realm":"hosting","enabled":true}' || true
curl -sf -o /dev/null -X POST ${KC}/admin/realms/hosting/roles -H "$H" -H "Content-Type: application/json" -d '{"name":"admin"}' || true
curl -sf -o /dev/null -X POST ${KC}/admin/realms/hosting/roles -H "$H" -H "Content-Type: application/json" -d '{"name":"user"}' || true
curl -sf -o /dev/null -X POST ${KC}/admin/realms/hosting/clients -H "$H" -H "Content-Type: application/json" -d '{"clientId":"panel-ui","enabled":true,"publicClient":true,"directAccessGrantsEnabled":true,"standardFlowEnabled":true,"redirectUris":["*"],"webOrigins":["*"],"protocol":"openid-connect"}' || true
curl -sf -o /dev/null -X POST ${KC}/admin/realms/hosting/clients -H "$H" -H "Content-Type: application/json" -d '{"clientId":"sftp-client","enabled":true,"publicClient":false,"secret":"__SFTP_SECRET__","directAccessGrantsEnabled":true,"standardFlowEnabled":false,"protocol":"openid-connect"}' || true
curl -sf -o /dev/null -X POST ${KC}/admin/realms/hosting/clients -H "$H" -H "Content-Type: application/json" -d '{"clientId":"mail-client","enabled":true,"publicClient":false,"secret":"__MAIL_SECRET__","directAccessGrantsEnabled":true,"standardFlowEnabled":false,"protocol":"openid-connect"}' || true
curl -sf -o /dev/null -X POST ${KC}/admin/realms/hosting/users -H "$H" -H "Content-Type: application/json" -d '{"username":"admin","enabled":true,"emailVerified":true,"firstName":"Admin","lastName":"User","email":"admin@localhost","credentials":[{"type":"password","value":"__KC_PASS__","temporary":false}]}' || true
UID=$(curl -sf ${KC}/admin/realms/hosting/users?username=admin -H "$H" | sed 's/.*"id":"\([^"]*\)".*/\1/')
RID=$(curl -sf ${KC}/admin/realms/hosting/roles/admin -H "$H" | sed 's/.*"id":"\([^"]*\)".*/\1/')
[ -n "$UID" ] && [ -n "$RID" ] && curl -sf -o /dev/null -X POST ${KC}/admin/realms/hosting/users/${UID}/role-mappings/realm -H "$H" -H "Content-Type: application/json" -d "[{\"id\":\"${RID}\",\"name\":\"admin\"}]" || true
SFTP_UUID=$(curl -sf ${KC}/admin/realms/hosting/clients?clientId=sftp-client -H "$H" | sed 's/.*"id":"\([^"]*\)".*/\1/')
[ -n "$SFTP_UUID" ] && curl -sf -o /dev/null -X POST ${KC}/admin/realms/hosting/clients/${SFTP_UUID}/protocol-mappers/models -H "$H" -H "Content-Type: application/json" -d '{"name":"realm roles flat","protocol":"openid-connect","protocolMapper":"oidc-usermodel-realm-role-mapper","config":{"multivalued":"true","access.token.claim":"true","claim.name":"roles","jsonType.label":"String"}}' || true
curl -sf -o /dev/null -X PUT ${KC}/admin/realms/hosting -H "$H" -H "Content-Type: application/json" -d '{"accessTokenLifespan":1800}' || true
echo "Setup complete."
ENDSCRIPT
sed -i "s/__KC_PASS__/${KC_PASS}/g; s/__SFTP_SECRET__/${SFTP_SECRET}/g; s/__MAIL_SECRET__/${MAIL_SECRET}/g" /tmp/kc-setup.sh

kubectl create configmap kc-setup-script -n $NS --from-file=setup.sh=/tmp/kc-setup.sh --dry-run=client -o yaml | kubectl apply -f - 2>/dev/null
kubectl delete job hosting-panel-keycloak-setup -n $NS --ignore-not-found 2>/dev/null
kubectl apply -f - <<EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: hosting-panel-keycloak-setup
  namespace: $NS
spec:
  backoffLimit: 5
  activeDeadlineSeconds: 120
  template:
    spec:
      restartPolicy: OnFailure
      volumes:
        - name: script
          configMap:
            name: kc-setup-script
            defaultMode: 0755
      containers:
        - name: setup
          image: ${GHCR}/mirror/curl:8.5.0
          imagePullPolicy: IfNotPresent
          command: ["/bin/sh", "/scripts/setup.sh"]
          volumeMounts:
            - name: script
              mountPath: /scripts
EOF
for i in $(seq 1 24); do
  kubectl get job hosting-panel-keycloak-setup -n $NS -o jsonpath='{.status.succeeded}' 2>/dev/null | grep -q 1 && break; sleep 5
done
ok "Keycloak realm configured"

# ── 8. Restart panel + pre-bind backup PVC ────────────────────────────────────
kubectl rollout restart deployment hosting-panel-panel -n $NS 2>&1 | tail -1
sleep 5
kubectl run pvc-trigger -n $NS --rm -i --restart=Never \
  --image=${GHCR}/mirror/busybox:1.36 --image-pull-policy=IfNotPresent \
  --overrides='{"spec":{"volumes":[{"name":"b","persistentVolumeClaim":{"claimName":"hosting-panel-backup-storage"}}],"containers":[{"name":"t","image":"'"${GHCR}"'/mirror/busybox:1.36","imagePullPolicy":"IfNotPresent","command":["sh","-c","echo bound"],"volumeMounts":[{"name":"b","mountPath":"/mnt"}]}]}}' \
  -- sh -c "echo bound" 2>/dev/null || true

# ── 9. Wait for all pods ─────────────────────────────────────────────────────
info "Waiting for all pods..."
sleep 20
kubectl get pods -n $NS | grep -v Completed

# ── Done ─────────────────────────────────────────────────────────────────────
echo ""
echo "============================================================"
echo -e "\033[1;32m  ✓ KubeCP installed successfully!\033[0m"
echo "============================================================"
echo ""
echo "  Panel URL:      http://${FQDN}:8080"
echo "  Keycloak URL:   http://keycloak.${FQDN}:8080"
echo ""
echo "  ── Credentials (save these!) ──"
echo "  Keycloak admin:     admin / ${KC_PASS}"
echo "  MariaDB root:       ${MARIADB_ROOT_PASS}"
echo "  PowerDNS API key:   ${PDNS_API_KEY}"
echo ""
echo "  Run e2e tests:"
echo "    bash e2e-test.sh --fqdn ${FQDN} --ip ${IP}"
echo "============================================================"
