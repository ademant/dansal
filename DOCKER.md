# Running dansal with Docker

Three images are published to the GitHub Container Registry on every push to `main` and on version tags:

| Image | Service | Default port |
|---|---|---|
| `ghcr.io/ademant/dansal` | REST API | 8000 |
| `ghcr.io/ademant/dansal-web` | Web frontend | 8080 |
| `ghcr.io/ademant/dansal-webmin` | Admin interface | 8090 |

---

## Quick start

```bash
# 1. Clone the repo and enter the docker directory
git clone https://github.com/ademant/dansal.git
cd dansal/docker

# 2. Create your .env file
cp .env.example .env
nano .env   # fill in domain, base URL, SMTP, webmin secret

# 3. Start all three services
docker compose up -d
```

After first start, create your first admin account (see [First-time setup](#first-time-setup)).

---

## Configuration

Dansal uses a two-layer configuration approach:

**Layer 1 — YAML files** (in `./config/`, mounted as `/etc/dansal/`): application settings that can be changed at runtime via dansal-webmin. The first time a container starts without an existing config file it copies a built-in default — so you only need to create files for settings you want to override.

**Layer 2 — Environment variables**: infrastructure values and secrets that differ per environment (domain, SMTP credentials, session secret). Env vars always take priority over the YAML file.

---

## Environment variables

### dansal (API)

| Variable | Config field | Default |
|---|---|---|
| `DANSAL_BASE_URL` | `server.base_url` | *(empty)* |
| `DANSAL_PORT` | `server.port` | `8000` |
| `DANSAL_DB_PATH` | `server.db_path` | `/var/lib/dansal/calendar.db` |
| `DANSAL_SMTP_HOST` | `smtp.host` | *(empty)* |
| `DANSAL_SMTP_PORT` | `smtp.port` | `587` |
| `DANSAL_SMTP_USER` | `smtp.username` | *(empty)* |
| `DANSAL_SMTP_PASS` | `smtp.password` | *(empty)* |
| `DANSAL_SMTP_FROM` | `smtp.from` | *(empty)* |

### dansal-web (frontend)

| Variable | Config field | Default |
|---|---|---|
| `DANSAL_WEB_DOMAIN` | `domain` | *(required)* |
| `DANSAL_WEB_BASE_URL` | `base_url` | `https://{domain}` |
| `DANSAL_WEB_DANSAL_URL` | `dansal_url` | *(required)* |
| `DANSAL_WEB_LISTEN` | `listen` | `:8080` |

The legacy names `DANSAL_DOMAIN` and `DANSAL_URL` are still accepted as fallbacks.

### dansal-webmin

| Variable | Config field | Default |
|---|---|---|
| `DANSAL_WEBMIN_SESSION_SECRET` | `session_secret` | *(required)* |
| `DANSAL_WEBMIN_DANSAL_URL` | `dansal_url` | `http://dansal:8000` |
| `DANSAL_WEBMIN_LISTEN` | `listen` | `:8090` |

Generate a secure session secret with:
```bash
openssl rand -hex 32
```

---

## Volumes

| Mount point | Purpose |
|---|---|
| `/etc/dansal` | YAML config files — shared between all three containers. dansal-webmin writes here when you save settings. |
| `/var/lib/dansal` | SQLite database (`calendar.db`) and uploaded images. Mount on persistent storage. |
| `/var/lib/dansal-web` | dansal-web database (`web.db`) and web-specific images (logo, banner). |

In the example `docker-compose.yml`:
- `./config` → `/etc/dansal` (shared across all three services)
- `./data` → `/var/lib/dansal`
- `./data-web` → `/var/lib/dansal-web`

---

## Upgrading

```bash
docker compose pull
docker compose up -d
```

The containers apply DB migrations automatically on startup.

---

## First-time setup

After starting the stack, create the first admin user via the API:

```bash
# Register an admin account (replace with your values)
curl -X POST http://localhost:8000/api/v1/register \
  -H "Content-Type: application/json" \
  -d '{"email":"admin@example.com","password":"changeme","display_name":"Admin"}'
```

Then promote the user to admin role using `dansal_admin`:

```bash
docker compose exec dansal /usr/local/bin/dansal_admin --config /etc/dansal/config.yaml \
  promote admin@example.com
```

Or via the webmin interface at `http://localhost:8090` once a second admin exists to promote the first.

---

## nginx reverse proxy

Put `dansal-web` behind nginx with TLS. Example `/etc/nginx/sites-available/dansal`:

```nginx
server {
    listen 443 ssl http2;
    server_name cal.example.com;

    ssl_certificate     /etc/letsencrypt/live/cal.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/cal.example.com/privkey.pem;

    location / {
        proxy_pass         http://127.0.0.1:8080;
        proxy_set_header   Host $host;
        proxy_set_header   X-Real-IP $remote_addr;
        proxy_set_header   X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header   X-Forwarded-Proto $scheme;
    }
}

server {
    listen 80;
    server_name cal.example.com;
    return 301 https://$host$request_uri;
}
```

Keep webmin (`port 8090`) on localhost only — do not expose it publicly without additional authentication.
