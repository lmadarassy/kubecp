# KubeCP

Kubernetes-native web hosting control panel — manage websites, databases, email, SFTP, DNS and TLS certificates through a single UI on k3s.

> ⚠️ **Proof of Concept** — This project is an early-stage experiment. It is not production-ready, not security-audited, and will break in ways you don't expect. Use it to explore the idea, not to host real workloads.

## What it does

KubeCP turns a single k3s node into a shared web hosting server (think cPanel/VestaCP, but Kubernetes-native). Every resource — website, database, email account — is a Kubernetes Custom Resource managed by an operator.

```
┌─────────────────────────────────────────────────────────┐
│                    Contour / Envoy                       │
│              (HTTPProxy ingress controller)               │
├──────────┬──────────┬──────────┬────────────────────────┤
│ Panel UI │ Panel API│ Keycloak │ phpMyAdmin / Roundcube  │
│ (Angular)│  (Go)    │  (OIDC)  │                        │
├──────────┴──────────┴──────────┴────────────────────────┤
│                  Hosting Operator                         │
│            (Website, Database, Email, SFTP CRDs)         │
├─────────┬──────────┬──────────┬──────────┬──────────────┤
│ MariaDB │ PowerDNS │ Postfix  │ Dovecot  │    SFTP      │
│ Galera  │          │ (SMTP)   │ (IMAP)   │              │
├─────────┴──────────┴──────────┴──────────┴──────────────┤
│                    Longhorn Storage                       │
└─────────────────────────────────────────────────────────┘
```

## Components

| Component | Tech | Description |
|---|---|---|
| **panel-core** | Go | REST API backend |
| **panel-ui** | Angular 19 | Web frontend |
| **hosting-operator** | Go / controller-runtime | Kubernetes operator for all CRDs |
| **panel-backup** | Bash | Backup/restore (files + DB dumps) |
| **sftp-image** | OpenSSH + Keycloak PAM | SFTP server with OIDC auth |
| **helm-chart** | Helm 3 | Single chart deploying everything |

## Quick Start

```bash
# On a fresh VM with k3s installed:
sudo bash install-k3s2-new.sh --fqdn panel.example.com --ip <NODE_IP>
```

Requires wildcard DNS (`*.panel.example.com`) pointing to the node. See [USER-GUIDE.md](USER-GUIDE.md) for details.

## Pre-built Images

Images are built by GitHub Actions and published to GHCR:

```
ghcr.io/lmadarassy/kubecp/panel-core:latest
ghcr.io/lmadarassy/kubecp/panel-ui:latest
ghcr.io/lmadarassy/kubecp/hosting-operator:latest
ghcr.io/lmadarassy/kubecp/panel-backup:latest
ghcr.io/lmadarassy/kubecp/sftp:latest
```

## What works (as of the PoC)

- ✅ Website creation with Nginx/PHP-FPM pods (PHP 8.4, 8.5)
- ✅ MariaDB database provisioning via CRD
- ✅ Email accounts (Postfix + Dovecot) via CRD
- ✅ SFTP access with Keycloak authentication
- ✅ DNS zone management (PowerDNS)
- ✅ TLS certificate management
- ✅ Backup & restore (files + databases)
- ✅ Multi-user with RBAC (Keycloak OIDC)
- ✅ Hosting plans (resource limits per user)
- ✅ 34/34 e2e tests passing

## What's missing for production

This is a non-exhaustive list of things that would need work:

- **Security hardening** — no network policies, no pod security standards, no secret encryption at rest
- **Multi-node** — tested on single-node k3s only
- **High availability** — single replicas everywhere, no failover
- **Resource limits** — hosting plan limits are stored but not enforced
- **Monitoring** — Prometheus endpoints exist but no dashboards or alerts
- **Email deliverability** — no DKIM/SPF/DMARC setup, no spam filtering
- **Upgrade path** — no migration strategy between versions
- **Documentation** — minimal, no API reference
- **Tests** — e2e tests cover happy paths only, no unit test coverage for the operator
- **Roundcube** — deployed but not fully functional

## License

MIT
