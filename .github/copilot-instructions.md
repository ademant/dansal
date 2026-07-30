# Copilot instructions for dansal

> Full project conventions live in `CLAUDE.md` at the repo root. This file adds the quick-reference summary that GitHub Copilot loads automatically.

---

## Quick commands (build / run / deploy)

```bash
make build                          # build all four binaries in parallel (always do this)
sudo make deploy INSTANCE=dev       # install binaries + restart dev instance
sudo make deploy INSTANCE=prod      # install binaries + restart prod instance
sudo scripts/install-instance       # first-time interactive instance setup
go vet ./...                        # static analysis (no test suite currently)
```

Never use `go build ./cmd/...` + manual install — always `make build && sudo make deploy INSTANCE=<name>`.

## Architecture

Four binaries under `cmd/`:

| Binary | Path | Role |
|---|---|---|
| `dansal` | `cmd/dansal/` | REST API server, SQLite DB |
| `dansal_web` | `cmd/dansal_web/` | Web frontend + ActivityPub |
| `dansal_admin` | `cmd/dansal_admin/` | Admin CLI |
| `dansal_webmin` | `cmd/dansal_webmin/` | Admin web UI |

- Templates: `cmd/dansal_web/templates/` (Go HTML templates, server-side rendered)
- Translations: `cmd/dansal_web/i18n.yaml` — **12 languages**: `br`, `ca`, `cs`, `de`, `en`, `es`, `fr`, `it`, `nl`, `pl`, `pt`, `uk`
- DB: SQLite at `/var/lib/dansal/<instance>/calendar.db`
- Config: `/etc/dansal/<instance>/{config.yaml, web.yaml, webmin.yaml}`
- Services (systemd): `dansal@<name>`, `dansal-web@<name>`, `dansal-webmin@<name>`

## Key rules — read before editing

- **All binaries together**: always build and deploy all four. Never selective deploy (issue #147).
- **DB migrations**: append idempotent blocks to `runMigrations()` in `cmd/dansal/main.go`; update `createTables()` for fresh installs; add a `pragma_table_info` safety-net after each block (see CLAUDE.md § DB migration safety-net pattern).
- **i18n**: new strings go in **all 12** language sections of `cmd/dansal_web/i18n.yaml`.
- **Maps**: use `attachTileLayer(map)` from `base.html` — never `L.tileLayer` directly.
- **Email / Telegram / Matrix**: always send in a goroutine — never block the HTTP handler.
- **Event deduplication** (5 tiers): UID → URL → location+start±3h → title+start±3h → fetch_source+fuzzy. Maintained in `previewDuplicateStatus()` and `insertEvent()`. Do not break the hierarchy.
- **Location aliases**: `locations.aliases` JSON array; append feed names when admin manually maps a location so future imports auto-match.
- **Unsaved-changes guard**: admin forms use `_formDirty` / `_markDirty()` / `safeGoBack()`. Keep this pattern when adding new admin forms.
- **Parent-child locations**: rooms inherit address/coordinates/parking from parent via `inheritLocationFields()`. Location dropdowns use `"Room — Building"` disambiguation labels.
- **`has_*` fields are legacy**: use tags instead (`has_ball` → `bal-folk`/`fest-noz`, etc.). See CLAUDE.md § has_* boolean fields.
- **No User-Agent sniffing for layout**: use CSS `@media` queries. UA detection fragments the HTTP cache.
- **Runtime config without restart**: use `site_settings` table + `siteSettingsCache` (10 s TTL), written by webmin UI.

## Where to look

| Feature area | Files |
|---|---|
| API routes + handlers | `cmd/dansal/main.go` and siblings |
| Web handlers | `cmd/dansal_web/frontend.go`, `admin_*.go` |
| Templates + JS | `cmd/dansal_web/templates/` |
| Event import / dedup | `admin_import.go`, `events.go`, `preview.go` in `cmd/dansal/` |
| DB migrations | `cmd/dansal/main.go` (`runMigrations`, `createTables`) |
| Translations | `cmd/dansal_web/i18n.yaml` |
| Runtime config | `cmd/dansal_web/sitecache.go`, `cmd/dansal_webmin/siteconfig.go` |
| CI/CD | `.github/workflows/release.yml`, `docker.yml` |
