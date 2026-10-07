---
description: Add a new column to a dansal API DB table — migration + safety net, createTables, struct, scan/insert/update, web form. No build/deploy.
argument-hint: <table> <column> <type> [DEFAULT value]
---

Args `$ARGUMENTS` → table, column, SQL type+default. Follow the `db-migration` skill (Shape A). Checklist:

1. Next version N: `grep -o 'if !applied([0-9]*)' cmd/dansal/main.go | tail -1` → +1.
2. `migrateDB()`: append `if !applied(N) { ALTER …; mark(N) }` + unconditional pragma_table_info safety net (index, if any, in the safety net).
3. `createTables()`: column in CREATE TABLE + `INSERT OR IGNORE INTO schema_migrations(version) VALUES(N)` in the catch-all.
4. Go struct (grep `json:"<existing column>"` to find it) — nullable int `*int`, text `string`.
5. Every SELECT/Scan (column order!), INSERT, UPDATE/PATCH path for that table; PATCH uses pointer fields.
6. If user-facing: `cmd/dansal_web` client struct (`dansal.go`), admin template + handler (`admin-ui` skill), labels via `add-i18n` skill.
7. Throwaway :memory: smoke test (createTables + migrateDB twice) → `go build ./... && go vet ./... && go test ./...`, `gofmt -l` touched files.
8. Commit only if this is an agreed issue (`Closes #N`); build/deploy only when asked.
