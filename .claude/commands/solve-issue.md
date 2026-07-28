---
description: Read a dansal GitHub issue, implement the fix, build, deploy, commit and close it.
argument-hint: <issue-number>
---

# Solve a dansal GitHub issue

Argument: `$ARGUMENTS` (issue number, e.g. `135`)

## Workflow

### 1. Read the issue

```bash
gh issue view $ARGUMENTS --json title,body,comments
```

If the issue number is missing, ask the user for it before continuing.

### 2. Explore the code

Based on the issue title and body, identify the relevant files. Common entry points:

| Topic | Files |
|---|---|
| Admin forms (event/location/org) | `cmd/dansal_web/templates/admin_*.html`, `cmd/dansal_web/admin_*.go` |
| Public frontend pages | `cmd/dansal_web/templates/*.html`, `cmd/dansal_web/frontend.go` |
| API / DB logic | `cmd/dansal/*.go` |
| DB migrations | `cmd/dansal/main.go` (`migrateDB`, `createTables`) |
| Email / board | `cmd/dansal/email.go`, `cmd/dansal/contact_posts.go` |
| iCal / feed import | `cmd/dansal_web/feed.go`, `cmd/dansal/admin_import.go` |
| Translations | `cmd/dansal_web/i18n.yaml` |
| ActivityPub | `cmd/dansal_web/activitypub.go` |
| Maps / dark mode | `cmd/dansal_web/templates/base.html` |
| Runtime config (no restart) | `cmd/dansal_web/sitecache.go`, `cmd/dansal_webmin/siteconfig.go` |

Read the relevant files before writing any code.

### 3. Check for i18n needs

If new UI strings are needed, use the `/add-i18n` skill or add manually to all **12** language sections of `cmd/dansal_web/i18n.yaml`. Section order (approximate line numbers, will shift as file grows):

| Lang | ~Line |
|---|---|
| `de` | 8 |
| `br` | 1107 |
| `en` | 2204 |
| `es` | 3304 |
| `fr` | 4391 |
| `it` | 5490 |
| `nl` | 6577 |
| `uk` | 7664 |
| `ca` | 8745 |
| `pt` | 9827 |
| `pl` | 10909 |
| `cs` | 11991 |

Use a nearby existing key as the anchor for each edit to ensure uniqueness.

### 4. Implement

Follow the project's established patterns:

- **Template helpers**: `derefInt`, `jsStr`, `isoDate`, `isoTime`, `locationsJSON`, `timetableLocationOptionsJSON` are in `cmd/dansal_web/frontend.go`'s `tmplFuncMap`.
- **Maps**: always use `attachTileLayer(map)` from `base.html` — never call `L.tileLayer` directly.
- **Location org IDs**: `Location.OrganizationIDs []int` (not `OrganizationID *int`).
- **Parent-child locations**: rooms inherit address/coordinates/parking from parent via `inheritLocationFields()`. Dropdowns use `"Room — Building"` labels via `locationsJSON`.
- **Unsaved-changes guard**: admin forms use `_formDirty` / `_markDirty()` / `safeGoBack()`. New forms must follow this pattern.
- **Email / Telegram / Matrix**: always send in a goroutine — never block the HTTP handler.
- **DB migrations**: use the `/add-db-field` skill, or append `if !applied(N) { … mark(N) }` to `migrateDB()` in `cmd/dansal/main.go`; update `createTables()`; add a safety-net check. Current highest version: **19**.
- **Rate limiting**: new admin POST routes need an entry in `routeEndpoint` in `cmd/dansal_web/user_rate_limit.go`.
- **Runtime config without restart**: use `site_settings` table + `siteSettingsCache`, not YAML config.
- **`has_*` fields**: legacy — switch any touched code path to tags (`has_ball` → `bal-folk`/`fest-noz`, etc.).
- **Save-and-stay pattern**: redirect to edit page with `?saved=1`; show `.form-saved` banner; back button uses `history.back()`.

### 5. Build

```bash
make build
```

Fix all compile errors before continuing.

### 6. Deploy to dev

```bash
sudo make deploy INSTANCE=dev
```

### 7. Commit and close

Stage only the files you changed. Commit message explains *why*, not *what*. Include `Closes #NNN`.

```bash
git add <files>
git commit -m "$(cat <<'EOF'
<type>: <short description>

<optional body>

Closes #$ARGUMENTS

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

## Key project facts

- **Go version**: 1.26+ required (`go version` to check)
- **DB**: SQLite at `/var/lib/dansal/<instance>/calendar.db`
- **Config**: `/etc/dansal/<instance>/{config.yaml, web.yaml, webmin.yaml}`
- **Services**: `dansal@<instance>` (API), `dansal-web@<instance>` (frontend), `dansal-webmin@<instance>` (admin UI)
- **Binaries**: installed to `/usr/lib/dansal/<instance>/` by `make deploy`
- **Always build and deploy all four binaries together** — never selective (issue #147)
- **`gh issue close`** can fail with a GraphQL permissions error — tell the user to close it manually if that happens
