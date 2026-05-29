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

`make deploy INSTANCE=<name>` installs binaries to `/usr/bin/`, updates systemd template units, and restarts the named instance (`dansal@<name>`, `dansal-web@<name>`, `dansal-webmin@<name>`). It does **not** build — run `make build` first as the regular user (sudo doesn't have `go` in PATH).

**Setting up a new instance** (idempotent — safe to re-run):
```bash
sudo make setup-instance INSTANCE=prod
# then edit /etc/dansal/prod/config.yaml, web.yaml, webmin.yaml
sudo systemctl start dansal@prod dansal-web@prod dansal-webmin@prod
sudo systemctl start dansal-fetch@prod.timer dansal-backup@prod.timer
```

Do **not** run `go build ./cmd/...` and install manually — always use `make build && sudo make deploy INSTANCE=<name>` to ensure all binaries are updated together.

## Project layout

| Path | Purpose |
|---|---|
| `cmd/dansal/` | REST API server (`/usr/bin/dansal`) |
| `cmd/dansal_web/` | Web frontend + ActivityPub (`/usr/bin/dansal-web`) |
| `cmd/dansal_admin/` | Admin CLI (`/usr/bin/dansal_admin`) |
| `cmd/dansal_web/templates/` | Go HTML templates |
| `cmd/dansal_web/i18n.yaml` | Translations (8 languages: `br`, `de`, `bzh`, `en`, `es`, `fr`, `it`, `nl`) |

## Key facts

- **DB**: SQLite at `/var/lib/dansal/calendar.db`; config at `/etc/dansal/config.yaml` (API) and `/etc/dansal/web.yaml` (web)
- **Services**: `dansal` (API, port 8000), `dansal-web` (frontend, port 8080 behind nginx)
- **DB migrations**: append idempotent `db.Exec(...)` calls at the end of `runMigrations()` in `cmd/dansal/main.go`; also update `createTables()` for fresh installs
- **Maps**: always use `attachTileLayer(map)` from `base.html` — never call `L.tileLayer` directly
- **Email**: always send in a goroutine — never block the HTTP handler
- **New i18n strings**: add to all 8 language sections in `i18n.yaml`
