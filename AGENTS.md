# Agent instructions for dansal

This file follows the `AGENTS.md` convention for AI coding agents (OpenAI Codex, etc.). It mirrors `CLAUDE.md` — if the two diverge, `CLAUDE.md` is authoritative.

---

## What this project is

**dansal** is a self-hosted bal-folk / folk-dance event calendar written in Go. It serves a REST API, a web frontend with ActivityPub federation, an admin CLI, and a web admin UI — all as four separate binaries built from a single monorepo.

## Build & deploy

```bash
# Build all four binaries (always build all together — never partial)
make build

# Deploy to an instance (installs binaries + restarts systemd units)
sudo make deploy INSTANCE=dev    # default after a change
sudo make deploy INSTANCE=prod   # only when explicitly requested

# First-time instance setup
sudo scripts/install-instance
```

**Go version**: 1.26+ required. Verify with `go version` before building.

Do **not** run `go build ./cmd/...` and copy binaries manually — always use `make build && sudo make deploy`.

## Project layout

| Path | Purpose |
|---|---|
| `cmd/dansal/` | REST API server |
| `cmd/dansal_web/` | Web frontend + ActivityPub |
| `cmd/dansal_admin/` | Admin CLI |
| `cmd/dansal_webmin/` | Admin web UI |
| `cmd/dansal_web/templates/` | Go HTML templates (server-side rendered) |
| `cmd/dansal_web/i18n.yaml` | Translations — 12 languages: `br`, `ca`, `cs`, `de`, `en`, `es`, `fr`, `it`, `nl`, `pl`, `pt`, `uk` |

- **DB**: SQLite at `/var/lib/dansal/<instance>/calendar.db`
- **Config**: `/etc/dansal/<instance>/{config.yaml, web.yaml, webmin.yaml}`
- **Services**: `dansal@<name>`, `dansal-web@<name>`, `dansal-webmin@<name>` (systemd template units)

## Rules every agent must follow

### Always
- Build and deploy **all four binaries together** — a selective deploy risks stale binaries (issue #147).
- Add new i18n strings to **all 12** language sections in `cmd/dansal_web/i18n.yaml`.
- Use `attachTileLayer(map)` from `base.html` for maps — never `L.tileLayer` directly.
- Send emails / Telegram / Matrix messages in a **goroutine** — never block the HTTP handler.
- After each DB migration block, add a `pragma_table_info` safety-net check (see below).

### Never
- Call `L.tileLayer` directly in templates or JS.
- Use User-Agent detection for layout decisions (use CSS `@media` queries instead).
- Skip the safety-net structural check after a migration block.
- Touch `has_ball` / `has_workshop` / `has_festival` without switching the code path to use tags.

## DB migrations

Append idempotent blocks to `runMigrations()` in `cmd/dansal/main.go`; update `createTables()` for fresh installs. After every migration block, add a structural safety-net:

```go
{
    var n int
    db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('table') WHERE name='column'").Scan(&n)
    if n == 0 {
        db.Exec("ALTER TABLE table ADD COLUMN column TYPE DEFAULT value")
    }
}
```

## Event deduplication (5 tiers)

`previewDuplicateStatus()` (`preview.go`) and `insertEvent()` (`events.go`) share this hierarchy — do not break it:

1. UID — exact feed UID match
2. URL — exact event URL match
3. `location_id` + `start_time ±3h` — same venue + slot (no title check needed)
4. `title` + `start_time ±3h` — fallback when location is unresolved
5. `fetch_source_id` + `start_time ±3h` + fuzzy title — flags for admin review instead of auto-merging

## Location aliases

`locations.aliases` (JSON array column) stores alternate names from external feeds. When an admin manually maps a feed location to a DB location, the feed name is appended as an alias so future imports auto-match. Preserve this behaviour in any import-related change.

## Parent-child locations

Rooms (`parent_id` set) inherit empty address/coordinates/parking from their parent via `inheritLocationFields()` in `cmd/dansal/locations.go`. In dropdowns, rooms are labelled `"RoomName — BuildingName"` for disambiguation.

## Unsaved-changes guard

Admin forms use:
- `_formDirty` — boolean, `false` at load
- `_markDirty()` — sets `_formDirty = true`, attaches `beforeunload`
- `safeGoBack()` — confirms if dirty, then calls `history.back()`

Apply this pattern when adding new admin forms.

## Runtime config (no restart)

Config that must change without restarting goes in the `site_settings` table (web DB), read via `siteSettingsCache` (10 s TTL) in `cmd/dansal_web/sitecache.go`. The webmin UI writes to it. Do not add such settings to YAML config files.

## Tags (not `has_*` booleans)

`has_ball`, `has_workshop`, `has_festival` are legacy columns. Use tags instead:

- `has_ball` → `bal-folk` or `fest-noz`
- `has_workshop` → `workshop`, `dance-workshop`, `musician-workshop`, or `music-course`
- `has_festival` → `festival`

When touching any `has_*` field, switch the code path to tags.

## DansalClient (cmd/dansal_web/dansal.go)

`DansalClient` proxies the REST API for the web frontend.

- Use `c.do(ctx, method, path, token, body, out, okStatus...)` for request + auth + status check + JSON decode in one call. Do **not** hand-roll `c.authed(...)` + `resp.StatusCode` switch + `json.NewDecoder` blocks — the ~90 identical blocks were consolidated into `do`.
- GET helpers: `c.get` (public), `c.getWithTotal` (captures `X-Total-Count`), `c.getWithTotalAuthed` (Bearer + `X-Total-Count`). Don't build per-endpoint GET helpers.
- `do` maps `404`→`errNotFound`, `403`→`errForbidden`, `410`→`errExpired`; compare with `errors.Is`.
- Whole-table lists use `limit=1000` (the `apiListLimit` const). Raise the const rather than special-casing.
- Keep `CreateLocation` as its manual block — it maps `409` to `LocationConflictError` (checked via `errors.As`). Keep `c.authed` only for endpoints needing custom status handling.

## Where to look

| What | Where |
|---|---|
| API routes + handlers | `cmd/dansal/main.go` and siblings |
| Web handlers | `cmd/dansal_web/frontend.go`, `admin_*.go` |
| Templates + JS | `cmd/dansal_web/templates/` |
| Event import / dedup | `admin_import.go`, `events.go`, `preview.go` in `cmd/dansal/` |
| DB migrations | `cmd/dansal/main.go` (`runMigrations`, `createTables`) |
| Translations | `cmd/dansal_web/i18n.yaml` |
| Runtime config | `cmd/dansal_web/sitecache.go`, `cmd/dansal_webmin/siteconfig.go` |
| Location parent/child | `cmd/dansal/locations.go` (`inheritLocationFields`) |
| CI/CD | `.github/workflows/release.yml`, `docker.yml` |
