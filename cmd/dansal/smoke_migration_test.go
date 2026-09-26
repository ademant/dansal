package main

import (
	"database/sql"
	"fmt"
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

// fetch_sources.type's CHECK constraint (#1377) must allow 'jcal' on both a
// fresh install and an existing DB that predates it (simulated by rebuilding
// the table with the pre-#1377 CHECK list, the exact shape
// migrateFetchSourcesKuferType left behind).
func TestSmokeMigrationFetchSourcesJcalType(t *testing.T) {
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	old := db
	db = conn
	t.Cleanup(func() { db = old; conn.Close() })

	n := 0
	insertJcal := func() error {
		n++
		_, err := conn.Exec("INSERT INTO fetch_sources (url, type) VALUES (?, 'jcal')", fmt.Sprintf("https://example.org/jcal-%d", n))
		return err
	}

	if err := createTables(); err != nil {
		t.Fatal(err)
	}
	migrateDB()
	if err := insertJcal(); err != nil {
		t.Fatalf("fresh install: 'jcal' rejected by CHECK constraint: %v", err)
	}

	// Existing DB from before this feature: rebuild with the exact shape
	// migrateFetchSourcesKuferType's own rebuild produces (pre-#1377 CHECK
	// list, but every other column a real existing DB would already have —
	// a table missing those would make the real migration's SELECT fail on
	// an unrelated column, which isn't what this test is checking).
	conn.Exec("DELETE FROM fetch_sources")
	conn.Exec("DROP TABLE fetch_sources")
	if _, err := conn.Exec(`CREATE TABLE fetch_sources (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		url TEXT UNIQUE NOT NULL,
		type TEXT NOT NULL DEFAULT 'ical' CHECK(type IN ('ical','json','folkdance-json','gancio-json','rss','kufer')),
		tags TEXT,
		organization_id INTEGER REFERENCES organizations(id) ON DELETE SET NULL,
		last_fetched_at INTEGER,
		last_result TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		template_id INTEGER,
		template_mode TEXT NOT NULL DEFAULT '',
		template_data TEXT,
		consecutive_failures INTEGER NOT NULL DEFAULT 0,
		dance_ids TEXT DEFAULT '[]',
		created_by_id INTEGER REFERENCES users(id),
		updated_at INTEGER,
		updated_by TEXT DEFAULT '',
		kufer_config TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	if err := insertJcal(); err == nil {
		t.Fatal("expected the pre-migration CHECK constraint to reject 'jcal'")
	}

	migrateDB()
	if err := insertJcal(); err != nil {
		t.Fatalf("after migration: 'jcal' still rejected: %v", err)
	}
	migrateDB() // idempotent
	if err := insertJcal(); err != nil {
		t.Fatalf("after idempotent re-migration: 'jcal' rejected: %v", err)
	}
}
