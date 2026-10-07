---
name: db-migration
description: Change the dansal API SQLite schema (cmd/dansal/main.go migrateDB/createTables): new column, table, index, widening a CHECK(col IN (...)) enum via table rebuild, data backfill. Use for any ALTER TABLE/CREATE TABLE/index or anything that must run on existing prod DBs. Not for dansal_web's web.db (see bottom).
---

# Schema changes (calendar.db)

FILES: `cmd/dansal/main.go` — `migrateDB()` (upgrades existing DBs; CLAUDE.md calls it runMigrations — wrong name) and `createTables()` (fresh installs; ends with a catch-all that pre-marks every version). Tests: `smoke_migration_test.go`, `smoke_migration_orphan_chk_test.go`. Package var `db *sql.DB` is swapped in tests.

## Shape A — new column (versioned block + safety net)
```go
// vN: <what/why> (#issue)
if !applied(N) {
    db.Exec("ALTER TABLE t ADD COLUMN c TYPE DEFAULT v")
    mark(N)
}
{ // safety net: runs even when createTables pre-marked N on an old DB
    var n int
    db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('t') WHERE name='c'").Scan(&n)
    if n == 0 { db.Exec("ALTER TABLE t ADD COLUMN c TYPE DEFAULT v") }
    db.Exec("CREATE INDEX IF NOT EXISTS idx_t_c ON t(c)") // indexes HERE, never in the version block
}
```
+ add column to `createTables()` CREATE TABLE + `INSERT OR IGNORE INTO schema_migrations(version) VALUES(N)` in its catch-all.

RULES
- Next N = current max `applied(…)` + 1. NEVER edit an existing block (already applied on prod → never reruns).
- Duplicate ALTER ADD COLUMN fails harmlessly; duplicate CREATE INDEX errors → always `IF NOT EXISTS`.
- Index on an existing column only: own version block with `CREATE INDEX IF NOT EXISTS`, no pragma check; also in createTables.

## Shape B — new table
Version block `db.Exec(xSchema)` + safety net checking `sqlite_master WHERE type='table' AND name='x'` → `db.Exec(xSchema)`, indexes in the safety net. Keep DDL in one const (`ownerMediaSchema` pattern); createTables carries its own copy — change both. Keep a permanent test modelled on `TestSmokeMigrationOwnerMedia` (fresh → drop table+version row → remigrate → drop table, version kept → remigrate → idempotent).
- Polymorphic owner tables (`owner_type`,`owner_id`, e.g. owner_media) have NO FK: every owner delete path must delete rows explicitly (collect child ids, e.g. rooms, BEFORE deleting), every merge path must fold/drop them; test each.

## Shape C — widen CHECK(col IN (...)) (table rebuild, no version number)
Copy the NEWEST reference (`migrateFetchSourcesJcalType`), not older ones.
```go
func migrateXNewValue() {
    var schema string
    db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='x'").Scan(&schema)
    if strings.Contains(schema, "'newval'") { return }
    if err := rebuildTable("x_chk", []string{
        `CREATE TABLE x_chk (... CHECK(col IN (..., 'newval')) ...)`,
        `INSERT INTO x_chk (<every current column>) SELECT <same> FROM x`,
        `DROP TABLE x`,
        `ALTER TABLE x_chk RENAME TO x`,
    }); err != nil { log.Printf("migrateXNewValue: %v", err); return }
    // recreate indexes (DROP TABLE loses them)
}
```
- MUST use `rebuildTable` (#1419): drops leftover shadow, one tx on a dedicated conn, foreign_keys toggled outside the tx. Loose statements → leftover `x_chk` → every startup fails "x_chk already exists".
- Call unconditionally from migrateDB; widen the CHECK in createTables too (fresh install then returns early).
- INSERT…SELECT must list every column existing at that point (incl. ones added by earlier widenings/ALTERs) or data is silently dropped.
- If the rebuild adds a column: add a pragma safety net after the call; carry existing values (`COALESCE(col,0)`).
- Test: hand-create the table with the OLD check and ALL real columns; assert new value rejected before, accepted after `migrateDB()` (`TestSmokeMigrationFetchSourcesJcalType`). Orphan/rollback refs: `TestSmokeMigrationOrphanedChkTable`, `TestRebuildTableRollsBack`.
- Known: prod/dev/test carry orphan `fetch_sources_chk`/`locations_chk` from before #1419 (journal noise on start).

## Verify (always before commit)
Throwaway `cmd/dansal/zz_smoke_test.go` (delete after unless risky):
```go
func TestSmokeMigration(t *testing.T) {
    conn, err := sql.Open("sqlite3", ":memory:"); if err != nil { t.Fatal(err) }
    old := db; db = conn; t.Cleanup(func() { db = old })
    if err := createTables(); err != nil { t.Fatal(err) }
    migrateDB(); migrateDB() // twice = idempotent
    var n int
    conn.QueryRow("SELECT COUNT(*) FROM pragma_table_info('t') WHERE name='c'").Scan(&n)
    if n == 0 { t.Fatal("missing") }
}
```
Real data: copy `/var/lib/dansal/<i>/calendar.db` (sudo cp) to scratchpad, point the test at the copy. Never write the live file.

## has_* columns
Never add `has_*`; touching existing ones → switch that path to tags (mapping in CLAUDE.md). Backfill pattern: `INSERT OR IGNORE INTO event_tags (event_id, tag) SELECT id,'bal-folk' FROM events WHERE has_ball=1`.

## web.db (dansal_web, /var/lib/dansal-web/<i>/web.db) is different
No migrateDB there: each package ensures its own schema (e.g. `places.EnsureSchema`). Make sure it's called at dansal-web startup, not only from webmin/admin paths — otherwise the table is missing after deploy (#1476: `no such table: postcodes`).
