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
| Admin forms (event/location/org) | `cmd/dansal_web/templates/admin_*.html`, `cmd/dansal_web/admin.go` |
| Public frontend pages | `cmd/dansal_web/templates/*.html`, `cmd/dansal_web/frontend.go` |
| API / DB logic | `cmd/dansal/*.go` |
| Email / board | `cmd/dansal/email.go`, `cmd/dansal/contact_posts.go` |
| iCal feeds | `cmd/dansal_web/feed.go` |
| Translations | `cmd/dansal_web/i18n.yaml` |
| ActivityPub | `cmd/dansal_web/activitypub.go` |
| Maps / dark mode | `cmd/dansal_web/templates/base.html` |

Read the relevant files before writing any code.

### 3. Check for i18n needs

If new UI strings are needed, add them to all 7 language sections in `cmd/dansal_web/i18n.yaml`. The sections are, in order: `br`, `de`, `en`, `es`, `fr`, `it`, `nl`. Use existing nearby keys as anchors to keep each edit unique. Each edit must match exactly one occurrence — use surrounding context lines if the key pattern repeats.

### 4. Implement

Follow the project's established patterns:

- **Template helpers**: `derefInt`, `jsStr`, `isoDate`, `isoTime`, `locationsJSON` are in `cmd/dansal_web/frontend.go`'s `tmplFuncMap`.
- **Maps**: always use `attachTileLayer(map)` from `base.html` — never call `L.tileLayer` directly.
- **Dark mode tiles**: handled automatically by `attachTileLayer`.
- **Location org IDs**: `Location.OrganizationIDs []int` (not `OrganizationID *int`).
- **Multi-day events**: `data-end-date` attribute on `<tr>` rows, handled by `renderWeek()` in `index.html`.
- **Email**: always send in a goroutine — never block the HTTP handler.
- **SMTP dial**: use `dialSMTPConn` from `email.go` (IPv4 fallback built in).
- **DB migrations**: append idempotent `db.Exec(...)` calls at the end of `runMigrations()` in `main.go`. Also update `createTables()` for fresh installs.
- **New join tables**: include the join table in both `createTables()` and `runMigrations()`.

### 5. Build

```bash
go build ./cmd/dansal/ ./cmd/dansal_web/
```

Fix all compile errors before continuing.

### 6. Deploy

```bash
sudo install -m 755 dansal /usr/bin/dansal && \
sudo install -m 755 dansal_web /usr/bin/dansal-web && \
sudo systemctl restart dansal dansal-web && \
echo "deployed"
```

If only `dansal_web` changed, skip the `dansal` install and restart. If only `dansal` changed, skip `dansal_web`.

### 7. Commit and push

Stage only the files you changed. Write a commit message that explains *why*, not *what*. End with `(closes #$ARGUMENTS)` in the subject line.

```bash
git add <files>
git commit -m "$(cat <<'EOF'
<type>: <description> (closes #$ARGUMENTS)

<optional body>

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
git push
```

## Key project facts

- **Binary paths**: `dansal_web` → `/usr/bin/dansal-web` (hyphen), `dansal` → `/usr/bin/dansal`
- **DB**: SQLite at `/var/lib/dansal/calendar.db`, config at `/etc/dansal/config.yaml`
- **Services**: `dansal` (API), `dansal-web` (frontend)
- **DB migrations**: always use `db.Exec(...)` without error checks — idempotency via `IF NOT EXISTS` / `OR IGNORE`
- **Go packages**: `cmd/dansal` (API server), `cmd/dansal_web` (web frontend), `cmd/dansal_admin` (admin CLI)
- **`gh issue close`** often fails with a GraphQL permissions error — tell the user to close it manually if that happens
