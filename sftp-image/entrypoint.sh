#!/bin/sh
set -e

# =============================================================================
# SFTP container entrypoint
# 1. Generate SSH host keys if missing
# 2. Write pam-keycloak-oidc config from env vars
# 3. Ensure /etc/passwd has entries for known users (via extrausers)
# 4. Start sshd in foreground
# =============================================================================

KEYS_DIR="/etc/ssh"
EXTRAUSERS_DIR="/var/lib/extrausers"
NEXT_UID=2000

# Generate host keys if not present
if [ ! -f "$KEYS_DIR/ssh_host_rsa_key" ]; then
    echo "[sftp-init] Generating SSH host keys..."
    ssh-keygen -t rsa     -b 4096 -f "$KEYS_DIR/ssh_host_rsa_key"     -N "" -q
    ssh-keygen -t ecdsa   -b 256  -f "$KEYS_DIR/ssh_host_ecdsa_key"   -N "" -q
    ssh-keygen -t ed25519         -f "$KEYS_DIR/ssh_host_ed25519_key" -N "" -q
    echo "[sftp-init] Host keys generated."
fi

# Write pam-keycloak-oidc config from environment variables
# The binary looks for <binary-name>.tml next to itself
cat > /opt/pam-keycloak-oidc/pam-keycloak-oidc.tml <<EOF
client-id = "${KEYCLOAK_CLIENT_ID:-sftp-client}"
client-secret = "${KEYCLOAK_CLIENT_SECRET:-}"
scope = "roles"
endpoint-auth-url = "${KEYCLOAK_URL:-http://keycloak:8080}/realms/${KEYCLOAK_REALM:-hosting}/protocol/openid-connect/auth"
endpoint-token-url = "${KEYCLOAK_URL:-http://keycloak:8080}/realms/${KEYCLOAK_REALM:-hosting}/protocol/openid-connect/token"
username-format = "%s"
vpn-user-role = "admin"
xor-key = "${XOR_KEY:-sftp-hosting-panel-xor-key-2026}"
EOF
echo "[sftp-init] pam-keycloak-oidc config written."

# Ensure directories exist
mkdir -p /home "$EXTRAUSERS_DIR"
touch "$EXTRAUSERS_DIR/passwd" "$EXTRAUSERS_DIR/group" "$EXTRAUSERS_DIR/shadow"

# ensure_user: create Linux user in both /etc/passwd and extrausers
# so that sshd's getpwnam() finds the user before PAM auth runs
ensure_user() {
    username="$1"
    if ! id "$username" >/dev/null 2>&1; then
        echo "[sftp-init] Creating user: $username (uid=$NEXT_UID)"
        useradd -M -d "/home/$username" -s /usr/sbin/nologin -u "$NEXT_UID" "$username" 2>/dev/null || true
        # Also add to extrausers for NSS lookup
        if ! grep -q "^${username}:" "$EXTRAUSERS_DIR/passwd" 2>/dev/null; then
            echo "${username}:x:${NEXT_UID}:${NEXT_UID}::/home/${username}:/usr/sbin/nologin" >> "$EXTRAUSERS_DIR/passwd"
            echo "${username}:x:${NEXT_UID}:" >> "$EXTRAUSERS_DIR/group"
            echo "${username}:!:19000:0:99999:7:::" >> "$EXTRAUSERS_DIR/shadow"
        fi
        NEXT_UID=$((NEXT_UID + 1))
    fi
}

# Create Linux users for each mounted home directory (/home/*)
for userdir in /home/*/; do
    if [ -d "$userdir" ]; then
        username=$(basename "$userdir")
        ensure_user "$username"
    fi
done

echo "[sftp-init] Starting sshd on port 2022..."
exec /usr/sbin/sshd -D -e
