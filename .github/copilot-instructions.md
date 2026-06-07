# Copilot instructions for dansal

Purpose: short, actionable guidance for Copilot CLI sessions and assistant agents working in this repository.

---

## Quick commands (build / run / deploy)

- Build all binaries (recommended):
  - make build
- Run locally (backend):
  - go run .
- Build single binary (local debugging only):
  - go build -o dansal ./cmd/dansal && ./dansal
- Deploy an instance (install binaries & restart services):
  - sudo make deploy INSTANCE=prod
- First-time instance setup (interactive):
  - sudo scripts/install-instance

Notes: always run `make build` before `make deploy`. Do NOT rely on `go build ./cmd/...` + manual install — the Make targets ensure all four binaries and packaging are kept consistent.

## Tests & lint

- This repository currently has no repository-wide test harness detected (no *_test.go found during inspection). If tests are added, run:
  - go test ./...            # run all tests
  - go test ./pkg/foo -run TestName  # run a single test or package
- Suggested quick checks (use when no CI linter present):
  - go vet ./...
  - go fmt ./...  (or `gofmt -w .` to apply)

If a linter is later added (golangci-lint, staticcheck), prefer the provided Make target or CI step instead of ad-hoc commands.

## High-level architecture (big picture)

- Monorepo with four main binaries under `cmd/`:
  - cmd/dansal — REST API + backend logic (SQLite DB)
  - cmd/dansal_web — web frontend and ActivityPub
  - cmd/dansal_admin — admin CLI
  - cmd/dansal_webmin — admin web UI
- Frontend templates and translations:
  - `cmd/dansal_web/templates/` — Go HTML templates
  - `cmd/dansal_web/i18n.yaml` — translations (7 languages: br, de, en, es, fr, it, nl)
- Database: SQLite per-instance at /var/lib/dansal/<instance>/calendar.db
- Configuration: per-instance YAML under /etc/dansal/<instance>/{config.yaml,web.yaml,webmin.yaml}
- Deployment model:
  - `make build` builds all binaries in one step
  - `make deploy INSTANCE=<name>` installs binaries to `/usr/lib/dansal/<name>/` and restarts systemd template units (`dansal@<name>`, `dansal-web@<name>`, `dansal-webmin@<name>`)

## Key conventions and repository-specific rules

- Always rebuild and redeploy all binaries together. Never ship a subset (see issue #147).
- DB migrations:
  - Append idempotent `db.Exec(...)` migration blocks to `runMigrations()` in `cmd/dansal/main.go`.
  - Update `createTables()` for fresh installs.
  - After each migration block, include a safety-net structural check (SELECT COUNT(*) FROM pragma_table_info...) that ALTERs or creates missing columns/tables if absent.
- Event deduplication: four-tier matching used by `previewDuplicateStatus()` and `insertEvent()` (UID, URL, title+location+start±3h, title+start±3h). Agents editing import logic should maintain the same hierarchy.
- Location aliases: `locations.aliases` is a JSON array used for auto-matching import names; when adding import mapping UI/logic, append feed names to aliases to improve future matches.
- Map tile usage: templates must call `attachTileLayer(map)` from `base.html`. Do not call `L.tileLayer` directly in code/templates — this centralizes tile-layer behaviour.
- Images: event images are stored as AVIF by default in the configured images dir and resized at upload time; served with `http.ServeFile` (no on-the-fly resizing).
- Email: always send emails in a goroutine (do not block HTTP handlers).
- i18n: when adding new strings, update all 7 language sections in `cmd/dansal_web/i18n.yaml`.
- UI/server data flow: `indexHandler` fetches events once and reuses the slice for map, weekly table and future list views — avoid additional queries or refetching in template helpers.

## Common places to look when asked about features

- API routes & handlers: `cmd/dansal/` (main.go and subpackages)
- Web frontend templates and JS: `cmd/dansal_web/templates/`
- Import/fetch code: `admin_import.go`, `events.go`, `preview.go` under `cmd/dansal/`
- Migrations: `cmd/dansal/main.go` (runMigrations/createTables)

## CI / GitHub workflows

- Release and Docker workflows exist under `.github/workflows/` (release.yml, docker.yml). Follow existing workflow patterns when adding CI changes.

---

If you want, merge or copy any additional operational notes from CLAUDE.md or dansal_admin.md into this file; they contain deploy and instance-setup guidance that is useful for agents.
