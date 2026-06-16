# Admin Guide - System Administration

Complete guide for installing, configuring, and maintaining dansal instances.

## Table of Contents

- [System Requirements](#system-requirements)
- [Installation](#installation)
- [Configuration](#configuration)
- [Updating an Existing Instance](#updating-an-existing-instance)
- [User Management](#user-management)
- [Backup and Recovery](#backup-and-recovery)
- [System Maintenance](#system-maintenance)
- [Troubleshooting](#troubleshooting)

## System Requirements

- **OS**: Linux with systemd (Debian/Ubuntu recommended)
- **Go**: 1.26+ (for building from source)
- **nginx**: Reverse proxy for TLS termination
- **certbot**: Let's Encrypt TLS certificates
- **openssl**: Required by the installer for secret generation and mTLS certificates

## Installation

### First-Time Setup

Run the interactive installer as root from the source directory:

```bash
sudo scripts/install-instance
```

The installer prompts for:
- **Instance name** (e.g. `dev`, `prod`) — used in all path and unit names
- **Ports** for API (default 8000), web (default 8080), and webmin (default 8090); auto-scans for free ports
- **Domains** for the web frontend, API, and optionally webmin
- **Mail**: local MTA (postfix/sendmail) or remote SMTP server
- **Instance identity**: description, maintainer name/email, security contact for federation metadata
- **mTLS**: optionally generates a CA and client certificate for webmin access
- **certbot**: optionally obtains Let's Encrypt certificates and configures nginx

After the installer completes:

```bash
# Review config files before starting
sudo nano /etc/dansal/<instance>/config.yaml
sudo nano /etc/dansal/<instance>/web.yaml
sudo nano /etc/dansal/<instance>/webmin.yaml

# Start timers when ready
sudo systemctl start dansal-fetch@<instance>.timer dansal-backup@<instance>.timer
```

### Manual Setup (without the script)

```bash
# 1. Create directories, install template configs, enable systemd units
sudo make setup-instance INSTANCE=prod

# 2. Edit the three config files
sudo nano /etc/dansal/prod/config.yaml
sudo nano /etc/dansal/prod/web.yaml
sudo nano /etc/dansal/prod/webmin.yaml

# 3. Build and install binaries
make build
sudo make deploy INSTANCE=prod

# 4. Set up nginx and TLS
certbot certonly --nginx -d events.example.com
certbot certonly --nginx -d api.example.com
sudo make deploy-nginx INSTANCE=prod

# 5. Start timers
sudo systemctl start dansal-fetch@prod.timer dansal-backup@prod.timer
```

### First Admin User

There is no auto-created admin account. Create the first admin via `dansal_admin`:

```bash
/usr/lib/dansal/<instance>/dansal_admin \
  --config /etc/dansal/<instance>/config.yaml \
  create-user --email admin@example.com --role admin
```

Add a password with `set-password`, or the user can set one via magic-link login.

### Branding

Before going live, drop custom `logo`, `banner`, and `favicon` files (`.svg`, `.avif`, `.jpg`, or `.gif`) into the instance's images directory (`/var/lib/dansal-web/<instance>/`). These are served immediately without a restart.

## Configuration

Each instance has three config files under `/etc/dansal/<instance>/`.

### API server: `config.yaml`

Key settings (all others have sensible defaults):

| Key | Description |
|---|---|
| `server.port` | TCP port the API listens on (default 8000) |
| `server.listen` | Bind address (default `127.0.0.1:<port>`) |
| `server.db_path` | SQLite database path |
| `server.images_dir` | Directory for uploaded event/musician/org images |
| `server.admin_socket` | Unix socket used by `dansal_admin` and `dansal-webmin` |
| `server.backup_dir` | Directory for database backups |
| `server.base_url` | Public API URL, used in emails and iCal feeds (required) |
| `server.token_expiration_hours` | Session lifetime in hours (default 24) |
| `server.invite_expiry_hours` | Invite link lifetime (default 48) |
| `server.rate_limit` | Requests per minute per IP (default 100) |
| `server.login_rate_limit` | Login attempts per minute per IP (default 5) |
| `server.login_max_failures` | Failed logins before account lock (default 10) |
| `server.login_failure_window_secs` | Rolling window for failure counting (default 600) |
| `server.admin_allowed_ips` | IPs allowed to reach `/api/v1/admin/*` (default loopback) |
| `server.allowed_origins` | CORS origins allowed to call the API (default: all) |
| `server.metrics_port` | Prometheus `/metrics` port (default 9090; 0 to disable) |
| `server.internal_shared_secret` | Shared secret with `dansal-web` to exempt it from rate limiting |
| `server.telegram_bot_token` | Telegram bot for event notifications (optional) |
| `server.matrix_homeserver` | Matrix bot for event notifications (optional) |
| `smtp.host` | Remote SMTP server hostname |
| `smtp.port` | SMTP port (default 587) |
| `smtp.username` | SMTP username |
| `smtp.password` | SMTP password (plain text; prefer `password_key`) |
| `smtp.password_key` | Path to file containing the SMTP password |
| `smtp.sendmail` | Local MTA binary path (takes precedence over `host` when set) |
| `smtp.from` | From address for outgoing email |
| `smtp.tls` | TLS mode: `starttls` (default), `tls`, or `none` |

### Web frontend: `web.yaml`

| Key | Description |
|---|---|
| `listen` | Bind address (default `127.0.0.1:8080`) |
| `domain` | Public domain name (used for ActivityPub actor URLs) |
| `dansal_url` | Internal URL of the dansal API |
| `internal_shared_secret` | Must match `server.internal_shared_secret` in `config.yaml` |
| `db_path` | SQLite database for ActivityPub keys and followers |
| `poll_secs` | How often to push new events to fediverse followers (default 300) |
| `pages_file` | Path to YAML file with per-language contact info and Impressum text |
| `i18n_file` | Path to YAML file overriding built-in translations |
| `relay_actor_name` | Name of the ActivityPub relay actor (default `relay`; set before first deploy) |
| `show_federated_events` | Show events from followed organisations on the main page |
| `nodeinfo_description` | Instance description for fediverse crawlers |
| `nodeinfo_maintainer_name` | Maintainer name for NodeInfo |
| `nodeinfo_maintainer_email` | Maintainer email for NodeInfo |
| `security_contact` | `mailto:` or `https:` URL for `/.well-known/security.txt` |
| `site_name` | Display name in navigation header (can also be set in the webmin UI) |
| `banner_height_main` | Banner height in pixels on the main page (0 to hide) |
| `dark_mode` | Colour scheme: `auto`, `light`, or `dark` |
| `telegram_webhook_secret` | Token to validate Telegram webhook calls |
| `telegram_bot_token` | Enable event suggestions with Telegram notification |
| `smtp_host` / `smtp_sendmail` | Enable event suggestions with email verification |
| `captcha_site_key` / `captcha_secret_key` | Cloudflare Turnstile for the suggestion form |

### Webmin: `webmin.yaml`

| Key | Description |
|---|---|
| `listen` | Bind address (default `127.0.0.1:8090`) |
| `dansal_url` | Internal URL of the dansal API |
| `admin_socket` | Must match `server.admin_socket` in `config.yaml` |
| `web_db_path` | Path to dansal-web's `web.db` (for site-config editing) |
| `instance` | Systemd instance name, used for the dashboard unit status |
| `site_name` | Display name in the webmin navigation header |
| `webmin_domain` | Public subdomain for `deploy-nginx-webmin` |

## Updating an Existing Instance

Always rebuild all four binaries together and deploy as a unit:

```bash
# Build (as regular user — sudo doesn't have go in PATH)
make build

# Deploy to a specific instance (installs binaries, restarts services)
sudo make deploy INSTANCE=prod
```

## User Management

### Creating Users

```bash
# Create a user (passwordless — they set one via magic link)
dansal_admin --config /etc/dansal/prod/config.yaml \
  create-user --email user@example.com --role publisher

# Create with a password
dansal_admin --config /etc/dansal/prod/config.yaml \
  create-user --email user@example.com --role admin --password <password>
```

### User Roles

| Role | Description |
|---|---|
| `admin` | Full system access, can manage everything |
| `publisher` | Can create/edit events, manage locations and musicians |
| `user` | Can create events for their own organization only |

### Other User Commands

```bash
dansal_admin list-users
dansal_admin set-role     --email E --role R
dansal_admin set-password --email E --password P
dansal_admin disable-user --email E
dansal_admin enable-user  --email E
dansal_admin delete-user  --email E
```

### Invite Links

Via the webmin interface: **Users → Create Invite**. Set the role and optional organization; the link expires after 48 hours (configurable via `server.invite_expiry_hours`).

Via CLI:
```bash
dansal_admin list-invites
dansal_admin revoke-invite --token TOKEN
```

### Session Management

```bash
dansal_admin list-sessions  --email E
dansal_admin revoke-session --id SESSION_ID
```

### mTLS Certificates for Webmin

After initial setup, issue additional client certificates:

```bash
dansal_admin --config /etc/dansal/prod/config.yaml \
  mtls-issue --email admin@example.com --days 1095
dansal_admin mtls-list
dansal_admin mtls-revoke --email user@example.com
```

### SMTP Configuration

```bash
dansal_admin smtp-show
dansal_admin smtp-set --host smtp.example.com --port 587 --username u@example.com
dansal_admin smtp-set-password
dansal_admin smtp-test --to test@example.com
```

## Backup and Recovery

### Automated Backups

The `dansal-backup@<instance>.timer` systemd timer runs `dansal_admin backup` on a schedule. Enable it during setup:

```bash
sudo systemctl enable --now dansal-backup@prod.timer
sudo systemctl status dansal-backup@prod.timer
```

Backups are written to `server.backup_dir` (default `/var/lib/dansal/<instance>/backups/`).

### Manual Backup

```bash
# Full backup: config + database + images (tar.gz)
dansal_admin --config /etc/dansal/prod/config.yaml backup

# Specify output path
dansal_admin --config /etc/dansal/prod/config.yaml backup --output /tmp/dansal-backup.tar.gz

# Encrypted backup (AES-256-GCM)
dansal_admin --config /etc/dansal/prod/config.yaml password-backup

# Incremental backup since a given time
dansal_admin --config /etc/dansal/prod/config.yaml \
  incremental-backup --since 2025-01-01T00:00:00Z
```

### Restore

```bash
# Restore from a backup archive (database restored live, no restart needed)
dansal_admin --config /etc/dansal/prod/config.yaml restore --input /path/to/backup.tar.gz

# Restore encrypted backup
dansal_admin --config /etc/dansal/prod/config.yaml password-restore --input /path/to/backup.enc
```

### Verifying Database Integrity

```bash
sqlite3 /var/lib/dansal/<instance>/calendar.db "PRAGMA integrity_check;"
```

## System Maintenance

### Database Vacuum

```bash
dansal_admin --config /etc/dansal/prod/config.yaml vacuum
```

### Feed Fetching

```bash
# Manually trigger a fetch of all configured feed sources
dansal_admin --config /etc/dansal/prod/config.yaml fetch-all

# Check the fetch timer
sudo systemctl status dansal-fetch@prod.timer
```

### Pruning Orphaned Images

```bash
dansal_admin --config /etc/dansal/prod/config.yaml prune-images
```

### Location Data Maintenance

```bash
# Fill missing address/town fields by parsing location names (dry-run by default)
dansal_admin --config /etc/dansal/prod/config.yaml fill-location-fields

# Apply changes
dansal_admin --config /etc/dansal/prod/config.yaml fill-location-fields --apply
```

### Logs

Services log to the systemd journal:

```bash
journalctl -u dansal@prod -f
journalctl -u dansal-web@prod -f
journalctl -u dansal-webmin@prod -f
```

## Troubleshooting

### Service Won't Start

```bash
# Check status and recent log output
sudo systemctl status dansal@prod
journalctl -u dansal@prod --since "5 min ago"
```

Common causes:
- **Port already in use**: Another process is on the configured port. Check with `ss -tlnp`.
- **Config file missing or malformed**: Confirm `/etc/dansal/prod/config.yaml` exists and has valid YAML; `server.base_url` must be set.
- **Binary not installed**: Run `make build && sudo make deploy INSTANCE=prod`.

### Database Issues

```bash
# Check disk space
df -h /var/lib/dansal/prod/

# Verify integrity
sqlite3 /var/lib/dansal/prod/calendar.db "PRAGMA integrity_check;"
```

### Login Problems

- **"Account disabled"**: Re-enable with `dansal_admin enable-user --email E`.
- **Account locked after failed attempts**: Wait for the `login_failure_window_secs` window to expire, or revoke sessions with `dansal_admin revoke-session`.
- **Passkey errors**: Verify `server.base_url` matches the actual origin (WebAuthn is origin-bound).

### Feed Import Issues

```bash
# Test a manual fetch to see error output
dansal_admin --config /etc/dansal/prod/config.yaml fetch-all

# Check the fetch timer log
journalctl -u dansal-fetch@prod -n 50
```

---

**Need help?** Open an issue on [GitHub](https://github.com/ademant/dansal/issues).

**Security issues?** Use the contact set in `security_contact` in `web.yaml`, or open a private GitHub advisory.
