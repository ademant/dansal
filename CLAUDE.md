# dansal — Claude Code project instructions

## Build & deploy

Always rebuild and redeploy **all** binaries together, regardless of which files changed. A selective deploy risks shipping stale binaries (see issue #147).

```bash
# Build all four binaries in parallel (as regular user)
make build

# Install binaries and restart a specific instance (requires sudo, no build step)
sudo make deploy INSTANCE=dev
sudo make deploy INSTANCE=prod
```

`make deploy INSTANCE=<name>` installs binaries to `/usr/lib/dansal/<name>/`, updates systemd template units, and restarts the named instance (`dansal@<name>`, `dansal-web@<name>`, `dansal-webmin@<name>`). Each instance has its own binary directory so dev/test/prod can run different versions independently. It does **not** build — run `make build` first as the regular user (sudo doesn't have `go` in PATH).

**Setting up a new instance** (first time only):
```bash
sudo scripts/install-instance
```
The script asks for ports, domain, SMTP, and optionally generates a webmin mTLS client certificate. It calls `make setup-instance` internally, patches the config files, and installs binaries.

Manual alternative (if you prefer not to use the script):
```bash
# 1. Create dirs, install template configs, enable units
sudo make setup-instance INSTANCE=prod

# 2. Edit the three config files
sudo nano /etc/dansal/prod/config.yaml   # port, base_url, smtp
sudo nano /etc/dansal/prod/web.yaml      # listen, domain, dansal_url
sudo nano /etc/dansal/prod/webmin.yaml   # listen, session_secret

# 3. Install binaries and start services
make build
sudo make deploy INSTANCE=prod
```

**Updating an existing instance:**
```bash
make build
sudo make deploy INSTANCE=prod
```

Do **not** run `go build ./cmd/...` and install manually — always use `make build && sudo make deploy INSTANCE=<name>` to ensure all binaries are updated together.

## Project layout

| Path | Purpose |
|---|---|
| `cmd/dansal/` | REST API server (`/usr/lib/dansal/<instance>/dansal`) |
| `cmd/dansal_web/` | Web frontend + ActivityPub (`/usr/lib/dansal/<instance>/dansal-web`) |
| `cmd/dansal_admin/` | Admin CLI (`/usr/lib/dansal/<instance>/dansal_admin`) |
| `cmd/dansal_web/templates/` | Go HTML templates |
| `cmd/dansal_web/i18n.yaml` | Translations (7 languages: `br`, `de`, `en`, `es`, `fr`, `it`, `nl`) |

## Key facts

- **DB**: SQLite at `/var/lib/dansal/calendar.db`; config at `/etc/dansal/config.yaml` (API) and `/etc/dansal/web.yaml` (web)
- **Services**: `dansal` (API, port 8000), `dansal-web` (frontend, port 8080 behind nginx)
- **DB migrations**: append idempotent `db.Exec(...)` calls at the end of `runMigrations()` in `cmd/dansal/main.go`; also update `createTables()` for fresh installs
- **Maps**: always use `attachTileLayer(map)` from `base.html` — never call `L.tileLayer` directly
- **Email**: always send in a goroutine — never block the HTTP handler
- **New i18n strings**: add to all 8 language sections in `i18n.yaml`
