# KubeCP — User Guide

Kubernetes-native web hosting control panel for managing websites, databases, email accounts, SFTP, DNS and TLS certificates on k3s clusters.

## Table of Contents

- [System Requirements](#system-requirements)
- [Architecture Overview](#architecture-overview)
- [Installation](#installation)
  - [Automated Installation (install.sh)](#automated-installation)
  - [Manual Installation (Helm)](#manual-installation-helm)
  - [Keycloak Realm Setup](#keycloak-realm-setup)
  - [Ingress / External Access](#ingress--external-access)
  - [DNS Entries](#dns-entries)
- [First Login](#first-login)
- [Using the Panel UI](#using-the-panel-ui)
  - [Dashboard](#dashboard)
  - [Website Management](#website-management)
  - [Database Management](#database-management)
  - [Email Accounts](#email-accounts)
  - [DNS Zone Management](#dns-zone-management)
  - [TLS Certificates](#tls-certificates)
  - [Backups](#backups)
  - [User Management (admin)](#user-management)
  - [Hosting Plans (admin)](#hosting-plans)
- [Additional Services](#additional-services)
  - [phpMyAdmin](#phpmyadmin)
  - [Roundcube Webmail](#roundcube-webmail)
  - [Keycloak Admin Console](#keycloak-admin-console)
- [REST API](#rest-api)
- [Uninstallation](#uninstallation)
- [Troubleshooting](#troubleshooting)

---

## System Requirements

| Requirement | Minimum |
|---|---|
| OS | Debian 11+, Ubuntu 20.04+, RHEL/CentOS/Rocky 8+, SLES 15+ |
| Kernel | >= 5.4 |
| CPU | 2 cores |
| RAM | 4 GB |
| Disk | 20 GB free |
| Kubernetes | >= 1.25 (k3s recommended) |
| Ports | 80, 443, 6443, 10250 |

### Included Components

The Helm chart automatically deploys the following sub-components (when enabled):

| Component | Description |
|---|---|
| **Keycloak** | Identity management (OIDC) |
| **MariaDB Galera** | Relational database cluster |
| **Longhorn** | Distributed block storage |
| **cert-manager** | Automatic TLS certificate management |
| **Contour/Envoy** | Ingress controller (HTTPProxy) |
| **MetalLB** | L2 Load Balancer for bare metal |
| **PowerDNS** | Authoritative DNS server |
| **phpMyAdmin** | Web-based database manager |
| **Roundcube** | Webmail client |

---

## Architecture Overview

```
┌─────────────────────────────────────────────────────────┐
│                    Contour / Envoy                       │
│              (HTTPProxy ingress controller)               │
├──────────┬──────────┬──────────┬────────────────────────┤
│ Panel UI │ Panel API│ Keycloak │ phpMyAdmin / Roundcube  │
│ (Angular)│  (Go)    │  (OIDC)  │                        │
├──────────┴──────────┴──────────┴────────────────────────┤
│                  Hosting Operator                         │
│          (Website, Database, Email, SFTP CRDs)           │
├─────────┬──────────┬──────────┬──────────┬──────────────┤
│ MariaDB │ PowerDNS │ Postfix  │ Dovecot  │    SFTP      │
│ Galera  │          │ (SMTP)   │ (IMAP)   │              │
├─────────┴──────────┴──────────┴──────────┴──────────────┤
│                    Longhorn Storage                       │
└─────────────────────────────────────────────────────────┘
```

---

## Installation

### Automated Installation

The `install.sh` script performs all steps automatically: system checks, k3s installation (if needed), Helm chart deployment, and Keycloak configuration.

#### FQDN Mode (recommended)

```bash
sudo bash install.sh --fqdn panel.example.com --ip <NODE_IP>
```

Requires wildcard DNS (`*.panel.example.com`) pointing to the node IP. Services are accessible on standard ports (80/443).

#### IP-only Mode (fallback)

```bash
sudo bash install.sh --ip <NODE_IP>
```

Services are accessible via Envoy NodePort (30080/30443). Subdomains use IP suffix (e.g. `keycloak.<IP>`).

#### Installation Steps

1. System requirements check (OS, kernel, CPU, RAM, disk, ports)
2. Existing Kubernetes cluster detection
3. k3s installation (if no cluster found) — without Traefik and ServiceLB
4. Longhorn prerequisites (open-iscsi, nfs-common)
5. Helm installation
6. Namespace creation (`hosting-system`)
7. Helm chart deployment with auto-generated passwords
8. Keycloak admin account creation in the `hosting` realm
9. Health check on all components
10. Summary with generated credentials

> **Note**: The script requires root privileges. Credentials are displayed only once at the end — save them!

---

### Manual Installation (Helm)

If you already have a running k3s/Kubernetes cluster with Helm:

#### 1. Create Namespace

```bash
kubectl create namespace hosting-system
```

#### 2. Update Helm Dependencies

```bash
cd helm-chart
helm dependency update
```

#### 3. Helm Install

```bash
helm install hosting-panel ./helm-chart \
  --namespace hosting-system \
  --set global.hostname=panel.example.com \
  --set keycloak.auth.adminPassword=<password> \
  --set mariadbGalera.rootUser.password=<password> \
  --set mariadbGalera.db.password=<password> \
  --set powerdns.api.key=<api-key> \
  --timeout 15m \
  --wait
```

#### 4. Verify Installation

```bash
kubectl get pods -n hosting-system
```

All pods should be in `Running` state.

---

### Keycloak Realm Setup

After Helm chart deployment, the Keycloak `hosting` realm, roles, and OIDC client are created automatically by a post-install Job. The Job:

- Creates the `hosting` realm
- Creates `admin` and `user` roles
- Creates the `panel-ui` OIDC public client
- Creates an `admin` user in the `hosting` realm
- Assigns the `admin` role

---

### Ingress / External Access

The platform uses Contour HTTPProxy resources for ingress. The Helm chart creates 4 HTTPProxy entries:

| HTTPProxy | FQDN | Backend |
|---|---|---|
| Panel | `<hostname>` | Panel UI (port 80) + Panel API (port 8080) |
| Keycloak | `keycloak.<hostname>` | Keycloak (port 80) |
| phpMyAdmin | `phpmyadmin.<hostname>` | phpMyAdmin (port 80) |
| Roundcube | `webmail.<hostname>` | Roundcube (port 80) |

#### TLS Certificates

In production, cert-manager automatically requests Let's Encrypt certificates. For test environments, generate self-signed certificates:

```bash
openssl req -x509 -nodes -days 365 -newkey rsa:2048 \
  -keyout /tmp/panel-tls.key -out /tmp/panel-tls.crt \
  -subj '/CN=panel.example.com' \
  -addext 'subjectAltName=DNS:panel.example.com,DNS:*.panel.example.com'

kubectl create secret tls panel-tls \
  --cert=/tmp/panel-tls.crt --key=/tmp/panel-tls.key \
  -n hosting-system
```

---

### DNS Entries

If you don't have a real DNS setup, add entries to your local hosts file:

**Linux/Mac** (`/etc/hosts`):
```
<NODE_IP>  panel.example.com  keycloak.panel.example.com  phpmyadmin.panel.example.com  webmail.panel.example.com
```

**Windows** (`C:\Windows\System32\drivers\etc\hosts`):
```
<NODE_IP>  panel.example.com  keycloak.panel.example.com  phpmyadmin.panel.example.com  webmail.panel.example.com
```

---

## First Login

1. Open your browser: `http://<hostname>/`
2. The Panel UI automatically redirects to the Keycloak login page
3. Log in with:
   - **Username**: `admin`
   - **Password**: the password shown at the end of the install script
4. After successful login, you'll see the Dashboard

---

## Using the Panel UI

The Panel UI is an Angular single-page application (SPA) that communicates with the Kubernetes cluster through the Go backend API.

### Dashboard

Overview page showing system status: number of websites, databases, email accounts, and DNS zones.

### Website Management

**Menu**: Websites

- **Create website**: specify domain name, PHP version, document root
- **Edit website**: modify settings
- **Delete website**: removes all associated Kubernetes resources

Behind the scenes, the system creates a `Website` CRD processed by the Hosting Operator:
- Starts an Nginx/PHP-FPM pod
- Creates a Longhorn PVC for files
- Configures SFTP access

### Database Management

**Menu**: Databases

- **Create database**: database name, username, password
- **Delete database**

The system creates a `Database` CRD that provisions the database and user in the MariaDB Galera cluster.

### Email Accounts

**Menu**: Emails

- **Create email account**: email address, password, quota
- **Delete email account**

The system creates an `EmailAccount` CRD that generates Postfix (SMTP) and Dovecot (IMAP/POP3) configuration.

### DNS Zone Management

**Menu**: DNS

- **Create zone**: automatic SOA, NS, A, MX records for the domain
- **Add record**: A, AAAA, CNAME, MX, TXT, SRV types
- **Edit/delete records**

DNS management uses the PowerDNS REST API.

### TLS Certificates

**Menu**: Certificates

- **Request certificate**: Let's Encrypt certificate for a domain (via cert-manager)
- **View certificate status**: Ready/NotReady
- **Delete certificate**

The system creates a cert-manager `Certificate` resource in Kubernetes.

### Backups

**Menu**: Backups

- **Start manual backup**
- **Restore from backup**
- **List backups**: timestamp, size, status

Backups include web files (from user PVCs) and database dumps (mysqldump).

### User Management

**Menu**: Users (admin only)

- **Create user**: username, email, password, role (admin/user), hosting plan
- **Edit user**: modify details, change role
- **Delete user**

Users are created in the Keycloak `hosting` realm.

### Hosting Plans

**Menu**: Hosting Plans (admin only)

- **Create plan**: name, disk quota, database limit, email limit, domain limit
- **Edit/delete plan**
- **Assign plan to user**

Plans are stored as `HostingPlan` CRDs in the cluster.

---

## Additional Services

### phpMyAdmin

**URL**: `http://phpmyadmin.<hostname>/`

Web interface for managing MariaDB Galera databases.

**Login**:
- **Server**: automatically configured for the Galera cluster
- **Username**: `root` / `<root_password>` or the database user created via the panel

### Roundcube Webmail

**URL**: `http://webmail.<hostname>/`

Web email client for reading and sending mail via the Dovecot IMAP server.

**Login**: use the email account credentials created in the Panel.

### Keycloak Admin Console

**URL**: `http://keycloak.<hostname>/admin/`

Keycloak admin interface for managing users, roles, OIDC clients, and realm settings.

**Login**:
- **Username**: `admin`
- **Password**: the Keycloak admin password from the install script output

---

## REST API

The Panel Core Go backend provides a REST API. All `/api/*` endpoints are protected by OIDC tokens (except `/api/health` and auth endpoints).

### Public Endpoints

| Method | Path | Description |
|---|---|---|
| GET | `/api/health` | Health check |
| GET | `/api/auth/login` | Initiate OIDC login |
| GET | `/api/auth/callback` | OIDC callback |
| POST | `/api/auth/logout` | Logout |
| POST | `/api/auth/refresh` | Token refresh |

### Protected Endpoints (OIDC token required)

| Resource | Path Prefix | Role |
|---|---|---|
| Websites | `/api/websites` | admin, user |
| Databases | `/api/databases` | admin, user |
| Email Accounts | `/api/email-accounts` | admin, user |
| DNS | `/api/dns` | admin, user |
| Certificates | `/api/certificates` | admin, user |
| Backups | `/api/backups` | admin, user |
| Import | `/api/backups/import` | admin |
| Users | `/api/users` | admin (list), mixed (own profile) |
| Hosting Plans | `/api/hosting-plans` | admin |

### Example API Call

```bash
# Get token from Keycloak
TOKEN=$(curl -s -X POST "http://keycloak.<hostname>/realms/hosting/protocol/openid-connect/token" \
  -d "client_id=panel-ui" \
  -d "username=admin" \
  -d "password=<YOUR_ADMIN_PASSWORD>" \
  -d "grant_type=password" | jq -r '.access_token')

# List websites
curl -s -H "Authorization: Bearer $TOKEN" \
  "http://<hostname>/api/websites" | jq
```

### Prometheus Metrics

```
GET /metrics
```

Prometheus-format metrics for monitoring systems.

---

## Uninstallation

### Automated Uninstallation

```bash
sudo bash uninstall.sh
```

Options:
- `--yes` / `-y`: skip confirmation
- `--keep-data`: preserve persistent data (PVCs, volumes)

### Uninstall Steps

1. Remove Helm release
2. Delete PVCs (unless `--keep-data`)
3. Delete CRDs (hosting, cert-manager, Longhorn, MetalLB, Contour)
4. Delete namespaces (`hosting-system`)

### Manual Uninstallation

```bash
# Remove Helm release
helm uninstall hosting-panel -n hosting-system --wait --timeout 10m

# Delete PVCs
kubectl delete pvc --all -n hosting-system

# Delete CRDs
kubectl delete crd websites.hosting.panel databases.hosting.panel \
  emailaccounts.hosting.panel sftpaccounts.hosting.panel hostingplans.hosting.panel

# Delete namespace
kubectl delete namespace hosting-system
```

---

## Troubleshooting

### Check Pod Status

```bash
kubectl get pods -n hosting-system
kubectl describe pod <pod-name> -n hosting-system
kubectl logs <pod-name> -n hosting-system
```

### Check HTTPProxy Status

```bash
kubectl get httpproxy -n hosting-system
kubectl describe httpproxy <name> -n hosting-system
```

If status is "invalid" with "Secret not found" error, the TLS secret is missing — see [TLS Certificates](#tls-certificates).

### Keycloak Not Reachable

```bash
kubectl logs hosting-panel-keycloak-0 -n hosting-system
kubectl get svc -n hosting-system | grep keycloak
```

### MariaDB Galera Won't Start

For single-node installations, bootstrap is required:

```yaml
mariadb-galera:
  galera:
    bootstrap:
      forceBootstrap: true
      bootstrapFromNode: 0
```

If the StatefulSet is stuck, delete PVCs and redeploy:

```bash
kubectl delete statefulset hosting-panel-mariadb-galera -n hosting-system
kubectl delete pvc -l app.kubernetes.io/name=mariadb-galera -n hosting-system
helm upgrade hosting-panel ./helm-chart -n hosting-system --reuse-values
```

### Panel UI Not Loading

1. Check that the panel pod is running:
   ```bash
   kubectl get pods -n hosting-system | grep panel
   ```
2. Check the service:
   ```bash
   kubectl get svc hosting-panel-panel -n hosting-system
   ```
3. Test directly from the cluster:
   ```bash
   curl -s http://<hostname>/ | head -5
   ```

### Useful Commands

```bash
# All resources in the namespace
kubectl get all -n hosting-system

# Helm release status
helm status hosting-panel -n hosting-system

# Helm release history
helm history hosting-panel -n hosting-system

# List CRDs
kubectl get websites,databases,emailaccounts,sftpaccounts -n hosting-system
```
