# Developer Guide

Guide for developers working with dansal's codebase, API, or contributing to the project.

## Table of Contents

- [Project Structure](#project-structure)
- [Development Setup](#development-setup)
- [Build Targets](#build-targets)
- [Testing](#testing)
- [API Overview](#api-overview)
- [Database Migrations](#database-migrations)
- [Coding Guidelines](#coding-guidelines)
- [Architecture](#architecture)
- [Contributing](#contributing)

## Project Structure

```
dansal/
├── cmd/
│   ├── dansal/           # REST API server (Go)
│   ├── dansal_web/       # Web frontend + ActivityPub (Go HTML templates + vanilla JS)
│   │   ├── templates/    # Go HTML templates
│   │   └── i18n.yaml     # Translations (br, de, bzh, en, es, fr, it, nl)
│   ├── dansal_admin/     # CLI administration tool
│   └── dansal_webmin/    # Admin web interface
├── packaging/            # Config file templates installed by make setup-instance
├── scripts/              # install-instance and other deployment helpers
├── go.mod
└── Makefile
```

The project has no `internal/` package — all code lives directly inside the respective `cmd/` package. The web frontend uses Go HTML templates rendered server-side; there is no separate JavaScript build step.

## Development Setup

### Prerequisites

- Go 1.26+
- Make
- SQLite 3.x (for inspecting the database; not needed to build)

### Building

```bash
# Clone repository
git clone https://github.com/ademant/dansal.git
cd dansal

# Build all four binaries
make build
```

Binaries are written to the project root: `dansal`, `dansal_web`, `dansal_admin`, `dansal_webmin`.

### Running Locally

Each binary reads a config file. The simplest dev workflow runs just the API server and the web frontend against a local SQLite file:

```bash
# Create a minimal config.yaml (see packaging/config.yaml for all options)
# Set server.base_url and smtp.* at minimum

go run ./cmd/dansal --config ./config.yaml &
go run ./cmd/dansal_web --config ./web.yaml
```

There is no `make dev` target or live-reload tooling baked in. Edit → `make build` → restart is the standard loop.

## Build Targets

| Target | Description |
|---|---|
| `make build` | Build all four binaries in parallel |
| `make build-dansal` | Build only the API server |
| `make build-dansal_web` | Build only the web frontend |
| `make build-dansal_admin` | Build only the admin CLI |
| `make build-dansal_webmin` | Build only the webmin interface |
| `make fmt` | Run `gofmt` across the codebase |
| `make vet` | Run `go vet` |
| `make vulncheck` | Run `govulncheck` |
| `make clean` | Remove build artifacts |
| `make deploy INSTANCE=<name>` | Install binaries to `/usr/lib/dansal/<name>/` and restart services (requires sudo) |
| `make deploy-nginx INSTANCE=<name>` | Generate and install nginx config for an instance (requires sudo) |
| `make deb` | Build a `.deb` package |

There are no cross-compile, Docker, or release make targets.

## Testing

Tests live alongside the source they test:

```
cmd/dansal/
├── events_test.go
├── template_roundtrip_test.go
└── dansal_test/           # integration test helpers
```

Run with standard Go tooling:

```bash
go test ./cmd/dansal/...
go test ./cmd/dansal_web/...
```

There is no `make test` target. Use `go test` directly. Tests that require a running database use an in-memory SQLite instance set up by the test harness in `cmd/dansal/dansal_test/`.

## API Overview

Full endpoint reference: **[API.md](API.md)**

The REST API is served by the `dansal` binary at `http://127.0.0.1:8000` by default (behind nginx in production).

dansal-web's own routes (public pages, feeds, embeds) are documented separately: **[WEB.md](WEB.md)**.

**Authentication:** Bearer token in `Authorization: Bearer <token>` header. Tokens are issued by `POST /api/v1/login`, magic link, or WebAuthn login. API keys (prefix `ak_`) are also accepted and have no expiration unless explicitly set.

**Three roles:** `admin`, `publisher`, `user`. There is no viewer role — unauthenticated access to public endpoints is the equivalent.

**Content negotiation:** Several GET endpoints accept `Accept: text/calendar` (iCalendar), `Accept: application/atom+xml` (Atom), or `Accept: application/geo+json` (GeoJSON) in addition to the default JSON.

## Database Migrations

dansal uses idempotent hand-written migrations, not a migration framework.

- **Append** new `db.Exec(...)` calls at the end of `runMigrations()` in `cmd/dansal/main.go`.
- **Also update** `createTables()` in the same file for fresh installs.
- Use the safety-net pattern for structural changes (column additions):

```go
// Safety net: ensure column exists even if migration was pre-marked
{
    var n int
    db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('table') WHERE name='column'").Scan(&n)
    if n == 0 {
        db.Exec("ALTER TABLE table ADD COLUMN column TYPE DEFAULT value")
    }
}
```

Migrations run automatically at startup. There is no `dansal_admin migrate` command.

## Coding Guidelines

### Go

- Follow [Effective Go](https://go.dev/doc/effective_go)
- `gofmt` for formatting; `go vet` before committing
- Keep HTTP handlers thin — business logic lives in helpers, not in handlers
- Always send email in a goroutine; never block an HTTP handler
- DB migrations: idempotent `IF NOT EXISTS` / `OR IGNORE`

### Templates and i18n

- Templates are in `cmd/dansal_web/templates/` — standard Go `html/template`
- All user-facing strings must be in `cmd/dansal_web/i18n.yaml` under all 8 language keys (`br`, `de`, `bzh`, `en`, `es`, `fr`, `it`, `nl`)
- Maps: always use `attachTileLayer(map)` from `base.html` — never call `L.tileLayer` directly

### JavaScript

The frontend uses vanilla JavaScript embedded in HTML templates. No build step, no bundler, no TypeScript. Keep script blocks minimal and self-contained within each template.

### Commit Messages

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add new event creation endpoint
fix: correct timezone handling in event display
docs: update API documentation for musicians
refactor: improve database query performance
```

## Architecture

```
Browser / fediverse / CLI
        │
        ├── nginx (TLS termination)
        │       │
        │       ├── dansal-web (port 8080) ─── Go HTML templates, ActivityPub, iCal
        │       │       │ (HTTP, loopback)
        │       └── dansal API (port 8000) ─── REST API, WebAuthn, email
        │               │
        │               └── SQLite (calendar.db)
        │
        └── dansal-webmin (port 8090, mTLS) ── Admin UI, unix socket → dansal API
```

- **dansal** (API server): REST JSON API, authentication, feed import, ActivityPub delivery coordination
- **dansal_web** (web frontend): Server-rendered HTML, ActivityPub actor/inbox, public-facing pages
- **dansal_admin** (CLI): Admin operations over a Unix socket to the running API — no direct DB access needed
- **dansal_webmin** (web admin UI): Browser-based admin panel, communicates with dansal API via loopback

All four binaries are statically linked Go executables. No Node.js, no separate frontend build pipeline. The SQLite database lives at `/var/lib/dansal/<instance>/calendar.db`.

## Contributing

1. Fork the repository and create a feature branch
2. Open a GitHub issue describing the problem and proposed solution before writing code
3. Run `make fmt && make vet` before committing
4. Write a clear commit message explaining *why*, not just *what*
5. Open a pull request referencing the issue (`Closes #NNN`)

**Bug reports**: [GitHub Issues](https://github.com/ademant/dansal/issues)

**Questions**: [GitHub Discussions](https://github.com/ademant/dansal/discussions)
