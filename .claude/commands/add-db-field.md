---
description: Add a new column to a dansal DB table — migration, safety-net, struct, createTables, build, deploy.
argument-hint: <table> <column> <type> [DEFAULT value]
---

# Add a DB field to dansal

Arguments: `$ARGUMENTS` — e.g. `locations parking TEXT DEFAULT ''`

Parse the argument into: **table**, **column**, **SQL type + default**.

---

## Step 1 — Find the current highest migration version

```bash
grep "if !applied(" cmd/dansal/main.go | tail -5
```

The next version is **N = highest + 1**. Also note the current highest in `createTables()`:

```bash
grep "INSERT OR IGNORE INTO schema_migrations" cmd/dansal/main.go | tail -3
```

## Step 2 — Append the migration block to `migrateDB()`

In `cmd/dansal/main.go`, find the end of the last `if !applied(…)` block and append immediately after it:

```go
if !applied(N) {
    db.Exec("ALTER TABLE <table> ADD COLUMN <column> <TYPE> DEFAULT <value>")
    mark(N)
}
// Safety net: ensure <column> exists even if vN was pre-marked by createTables.
{
    var n int
    db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('<table>') WHERE name='<column>'").Scan(&n)
    if n == 0 {
        db.Exec("ALTER TABLE <table> ADD COLUMN <column> <TYPE> DEFAULT <value>")
    }
}
```

The safety-net block is unconditional — it runs on every startup at near-zero cost once the column exists, and self-heals on legacy DBs where `createTables()` pre-marked the version without running the ALTER.

## Step 3 — Update `createTables()` for fresh installs

In `cmd/dansal/main.go`, find the `CREATE TABLE IF NOT EXISTS <table>` statement inside `createTables()` and add the column there too. Also add the new version to the `INSERT OR IGNORE INTO schema_migrations` list at the end of `createTables()`:

```go
db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(N)")
```

## Step 4 — Update the Go struct

Find the struct that maps to this table (usually in `cmd/dansal/<table_singular>.go` or `cmd/dansal/models.go`) and add the field with the correct JSON tag:

```go
MyField string `json:"my_field"`
```

For nullable integers use `*int`; for nullable text use `*string` or `string` (empty = unset).

## Step 5 — Update the API handler

Find where the struct is read from the DB (usually a `Scan(…)` call) and written to the DB (usually an `INSERT` or `UPDATE`). Add the new field to both.

- **Scan**: add `&row.MyField` in column order.
- **INSERT / UPDATE**: add the column name and `?` placeholder; add the value to the args slice.

## Step 6 — Update the web layer (if user-facing)

If the field appears in admin forms or public pages:

- **Template**: add the input field to `cmd/dansal_web/templates/admin_<table>.html`.
- **Handler**: read `r.FormValue("my_field")` in `cmd/dansal_web/admin_<table>.go` and pass it through.
- **i18n**: if new label strings are needed, use `/add-i18n`.

## Step 7 — Build and verify

```bash
make build
```

Fix all compile errors. The build must be clean before deploying.

## Step 8 — Deploy to dev

```bash
sudo make deploy INSTANCE=dev
```

## Step 9 — Commit

```bash
git add cmd/dansal/main.go <any other changed files>
git commit -m "$(cat <<'EOF'
feat: add <table>.<column> field

<why this field is needed>

Closes #NNN

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Quick reference — safety-net variants

**Column on an existing table:**
```go
{
    var n int
    db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('table') WHERE name='column'").Scan(&n)
    if n == 0 {
        db.Exec("ALTER TABLE table ADD COLUMN column TYPE DEFAULT value")
    }
}
```

**Seed/lookup data (e.g. tags):**
```go
{
    var n int
    db.QueryRow("SELECT COUNT(*) FROM table").Scan(&n)
    if n == 0 {
        db.Exec("INSERT OR IGNORE INTO table ...")
    }
}
```

**New table entirely:** add `CREATE TABLE IF NOT EXISTS` to both `migrateDB()` (in the versioned block) and `createTables()`.
