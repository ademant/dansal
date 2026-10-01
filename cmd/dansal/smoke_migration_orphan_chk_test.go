package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// openTempDB swaps the package db for a fresh file-backed SQLite DB. A file
// (not :memory:) so rebuildTable's dedicated db.Conn sees the same database.
func openTempDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "calendar.db"))
	if err != nil {
		t.Fatal(err)
	}
	old := db
	db = conn
	t.Cleanup(func() { db = old; conn.Close() })
	return conn
}

func tableExists(conn *sql.DB, name string) bool {
	var n int
	conn.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n)
	return n > 0
}

// TestSmokeMigrationOrphanedChkTable covers #1419: dev was stuck with a
// fetch_sources_chk left behind by a failed rebuild (an older build's jcal
// shadow, still carrying dance_ids), so every later fetch_sources rebuild
// failed with "table fetch_sources_chk already exists" and imported_once
// never arrived — every fetchSourceCols query 500'd. Startup must now clear
// the orphan, finish the jcal + jsonld widenings and keep the existing rows.
func TestSmokeMigrationOrphanedChkTable(t *testing.T) {
	conn := openTempDB(t)
	if err := createTables(); err != nil {
		t.Fatal(err)
	}
	migrateDB()

	// Reproduce dev's state: pre-jcal fetch_sources (no imported_once) with
	// rows, plus the orphaned shadow from the failed run.
	conn.Exec("DROP TABLE fetch_sources")
	for _, q := range []string{
		`CREATE TABLE fetch_sources (
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
			created_by_id INTEGER REFERENCES users(id),
			updated_at INTEGER,
			updated_by TEXT DEFAULT '',
			kufer_config TEXT,
			category_filter TEXT
		)`,
		`INSERT INTO fetch_sources (url, type, category_filter) VALUES ('https://example.org/a.ics', 'ical', 'ball'), ('https://example.org/k', 'kufer', NULL)`,
		`CREATE TABLE fetch_sources_chk (id INTEGER PRIMARY KEY, url TEXT, dance_ids TEXT DEFAULT '[]')`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	migrateDB()

	if tableExists(conn, "fetch_sources_chk") {
		t.Error("orphaned fetch_sources_chk still present after migration")
	}
	var schema string
	conn.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='fetch_sources'").Scan(&schema)
	if !strings.Contains(schema, "'jcal'") || !strings.Contains(schema, "'jsonld'") {
		t.Errorf("fetch_sources CHECK not widened: %s", schema)
	}
	var rows int
	var filter string
	conn.QueryRow("SELECT COUNT(*) FROM fetch_sources").Scan(&rows)
	conn.QueryRow("SELECT COALESCE(category_filter,'') FROM fetch_sources WHERE url='https://example.org/a.ics'").Scan(&filter)
	if rows != 2 || filter != "ball" {
		t.Errorf("rows=%d category_filter=%q, want 2 rows and data kept", rows, filter)
	}
	// The query behind GET /api/v1/fetchurl must work again.
	if _, err := conn.Exec("SELECT " + fetchSourceCols + " FROM fetch_sources fs"); err != nil {
		t.Errorf("fetchSourceCols query still fails: %v", err)
	}

	migrateDB() // idempotent
	if tableExists(conn, "fetch_sources_chk") {
		t.Error("fetch_sources_chk reappeared on second migrateDB")
	}
}

// TestRebuildTableRollsBack covers #1419's other half: a rebuild failing
// mid-sequence must leave neither the shadow table nor a half-done swap.
func TestRebuildTableRollsBack(t *testing.T) {
	conn := openTempDB(t)
	conn.Exec("CREATE TABLE things (id INTEGER PRIMARY KEY, name TEXT)")
	conn.Exec("INSERT INTO things (name) VALUES ('a'), ('b')")

	err := rebuildTable("things_chk", []string{
		"CREATE TABLE things_chk (id INTEGER PRIMARY KEY, name TEXT CHECK(name IN ('a','b')))",
		"INSERT INTO things_chk (id, name) SELECT id, no_such_column FROM things",
		"DROP TABLE things",
		"ALTER TABLE things_chk RENAME TO things",
	})
	if err == nil {
		t.Fatal("expected rebuild to fail on the missing column")
	}
	if tableExists(conn, "things_chk") {
		t.Error("shadow table left behind after failed rebuild")
	}
	var n int
	conn.QueryRow("SELECT COUNT(*) FROM things").Scan(&n)
	if n != 2 {
		t.Errorf("original table damaged: %d rows, want 2", n)
	}
}
