---
name: db-migration
description: Add or modify the dansal SQLite schema (new columns, tables, indexes) safely. Use when touching the events/locations/organizations/timetable_entries schema, adding an ALTER TABLE, changing createTables(), or when a change needs to run on existing production databases. Encodes the migrateDB() version-block pattern, the pragma_table_info safety-net, createTables() sync for fresh installs, and the throwaway :memory: smoke test.
---

# Safe DB migrations in dansal

All schema for the dansal calendar DB lives in `cmd/dansal/main.go`. Two functions must stay in sync:

- **`createTables()`** (`cmd/dansal/main.go:2889`) — full `CREATE TABLE IF NOT EXISTS` schema, used for **fresh installs** only.
- **`migrateDB()`** (`cmd/dansal/main.go:857`) — idempotent versioned blocks that upgrade **existing** databases.

SQLite is single-file; every instance (`dev`, `prod`, …) has its own DB at `/var/lib/dansal/<instance>/calendar.db`. A migration that fails on an existing instance bricks the deployment, so the "always safe on a re-run" rule is non-negotiable.

## The version-block pattern

`migrateDB()` tracks applied versions in the `schema_migrations` table via two local closures:

```go
applied := func(v int) bool { /* SELECT COUNT(*) FROM schema_migrations WHERE version=? */ }
mark := func(v int) { /* INSERT OR IGNORE INTO schema_migrations(version) VALUES(?) */ }
```

Find the current highest version, then append the next `N`:

```go
// v25: <what it adds and why, with the issue number in the style "#NNN">.
if !applied(25) {
    db.Exec("ALTER TABLE events ADD COLUMN foo TEXT DEFAULT ''")
    mark(25)
}
```

Rules for the block body:
- **Bump the version** — never edit an existing `applied(N)` block to change its meaning; a version that's already in `schema_migrations` on prod will never re-run. Append a new block.
- **`ALTER TABLE` silently fails** when the column already exists, but a duplicate `CREATE INDEX` errors — use `CREATE INDEX IF NOT EXISTS`.
- **Never create an index unconditionally on a column added in the same block** — on an existing DB the column doesn't exist yet at that point in the script, and `createTables()`'s catch-all pre-marks the version (see below). Put the index creation in the safety-net block instead.

## The safety-net block — ALWAYS, after every migration

After each version block, add a structural check so the column/table exists even when the version was pre-marked:

```go
{
    var n int
    db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('table') WHERE name='column'").Scan(&n)
    if n == 0 {
        db.Exec("ALTER TABLE table ADD COLUMN column TYPE DEFAULT value")
    }
    db.Exec("CREATE INDEX IF NOT EXISTS idx_... ON table(column)") // indexes go here, not in the version block
}
```

- One `pragma_table_info` check per new column; the index creation goes in this block too (see real example: v18, `timetable_entries.instructor_id`, `cmd/dansal/main.go:1611-1628`).
- This is the rule AGENTS.md calls the "safety-net structural check" — **never skip it**, even if the version block looks complete.

## `createTables()` — keep fresh installs identical

Fresh installs must end up with the same final schema as migrated instances, or the catch-all breaks:

- Add the new column/table to the `CREATE TABLE` statements in `createTables()`.
- Append `db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(N)")` for the new version in the **catch-all block** at the bottom of `createTables()` (`cmd/dansal/main.go:3409` onwards) — this pre-marks the version so the corresponding `migrateDB()` block is skipped on fresh installs. That is why the safety-net block is essential: it runs even when the version block is skipped.
- v1 is special-cased: `createTables()` marks it applied, and a comment at `migrateDB()` (`main.go:867-870`) explains that re-running is harmless because every statement is idempotent.

## Verify with the throwaway smoke test

Before committing any schema change, write a one-off `_test.go` in `cmd/dansal`:

```go
func TestSmokeMigration(t *testing.T) {
    conn, err := sql.Open("sqlite3", ":memory:")
    if err != nil { t.Fatal(err) }
    old := db
    db = conn
    t.Cleanup(func() { db = old })
    if err := createTables(); err != nil { t.Fatal(err) }
    migrateDB() // must succeed on the fresh DB
    migrateDB() // run TWICE to confirm idempotency on a second boot
    var n int
    conn.QueryRow("SELECT COUNT(*) FROM pragma_table_info('table') WHERE name='column'").Scan(&n)
    if n == 0 { t.Fatal("column missing after migration") }
}
```

The package-level `var db *sql.DB` (`cmd/dansal/main.go:27`) is swapped directly; existing tests use the same pattern (`dedup_test.go:16`, `locations_scan_test.go:17`). Delete the throwaway file afterward unless the change is risky enough to keep permanently.

## The `has_*` trap

`has_ball`, `has_workshop`, `has_festival` are **legacy columns** — do not add new `has_*` columns and do not touch them without switching the code path to tags:

- `has_ball` → tag `bal-folk` or `fest-noz`
- `has_workshop` → tag `workshop`, `dance-workshop`, `musician-workshop`, or `music-course`
- `has_festival` → tag `festival`

Existing tag backfill pattern: `INSERT OR IGNORE INTO event_tags (event_id, tag) SELECT id, 'bal-folk' FROM events WHERE has_ball = 1` (`main.go:1220-1221`).

## Final checks

```bash
go build ./...
go vet ./...
go test ./...
```

Then build + deploy all binaries together: `make build` and `sudo make deploy INSTANCE=dev` (see the `deploy` skill).
