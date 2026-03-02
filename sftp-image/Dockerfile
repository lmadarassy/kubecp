# =============================================================================
# SFTP image: Debian + OpenSSH + pam-keycloak-oidc for Keycloak authentication
# Auth flow: SFTP login → sshd → PAM → pam-keycloak-oidc → Keycloak ROPC → OK/FAIL
# Debian-based for proper PAM support (alpine sshd.pam has session issues).
# =============================================================================

# --- Stage 1: Build pam-keycloak-oidc from source ---
FROM golang:1.24-bookworm AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
      git libpam0g-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build
RUN git clone --depth 1 https://github.com/zhaow-de/pam-keycloak-oidc.git .
RUN go build -o pam-keycloak-oidc .

# --- Stage 2: Runtime image ---
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
      openssh-server \
      openssh-sftp-server \
      libpam-modules \
      libnss-extrausers \
      ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /run/sshd /opt/pam-keycloak-oidc /var/lib/extrausers \
    && touch /var/lib/extrausers/passwd /var/lib/extrausers/group /var/lib/extrausers/shadow

# Enable extrausers NSS — allows dynamic user creation for Keycloak-authenticated users
# sshd calls getpwnam() before PAM auth; extrausers provides the user entry
RUN sed -i 's/^passwd:.*/passwd:         files extrausers/' /etc/nsswitch.conf \
    && sed -i 's/^group:.*/group:          files extrausers/' /etc/nsswitch.conf \
    && sed -i 's/^shadow:.*/shadow:         files extrausers/' /etc/nsswitch.conf

# Copy built binary
COPY --from=builder /build/pam-keycloak-oidc /opt/pam-keycloak-oidc/pam-keycloak-oidc
RUN chmod +x /opt/pam-keycloak-oidc/pam-keycloak-oidc

# PAM config for sshd
COPY pam-sshd /etc/pam.d/sshd
COPY sshd_config /etc/ssh/sshd_config
COPY ensure-user.sh /opt/pam-keycloak-oidc/ensure-user.sh
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh /opt/pam-keycloak-oidc/ensure-user.sh

EXPOSE 2022

ENTRYPOINT ["/entrypoint.sh"]
