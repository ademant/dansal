package main

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestSmokeMigrationPendingFetchSuggestions(t *testing.T) {
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	old := db
	db = conn
	t.Cleanup(func() { db = old; conn.Close() })

	if err := createTables(); err != nil {
		t.Fatal(err)
	}
	migrateDB()
	migrateDB() // idempotency on a second boot

	var n int
	conn.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='pending_fetch_suggestions'").Scan(&n)
	if n == 0 {
		t.Fatal("pending_fetch_suggestions table missing after migration")
	}
}

// owner_media (#1360/#1361) must exist on fresh installs, be created by the
// v43 migration on an existing DB that predates it, and self-heal when the
// version was pre-marked without the table (createTables' catch-all).
func TestSmokeMigrationOwnerMedia(t *testing.T) {
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	old := db
	db = conn
	t.Cleanup(func() { db = old; conn.Close() })

	hasTable := func() bool {
		var n int
		conn.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='owner_media'").Scan(&n)
		return n > 0
	}

	if err := createTables(); err != nil {
		t.Fatal(err)
	}
	migrateDB()
	if !hasTable() {
		t.Fatal("owner_media missing on fresh install")
	}

	// Existing DB from before the feature: no table, v43 not yet applied.
	conn.Exec("DROP TABLE owner_media")
	conn.Exec("DELETE FROM schema_migrations WHERE version = 43")
	migrateDB()
	if !hasTable() {
		t.Fatal("owner_media not created by the v43 migration")
	}

	// Pre-marked but table missing: the safety net must recreate it.
	conn.Exec("DROP TABLE owner_media")
	migrateDB()
	if !hasTable() {
		t.Fatal("safety net did not recreate owner_media")
	}
	migrateDB() // idempotent
}
