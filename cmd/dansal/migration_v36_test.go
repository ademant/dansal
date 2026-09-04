package main

import (
	"database/sql"
	"testing"
)

// TestMigrationV36FetchSourceIDIndex covers #1246: findExistingEvent's tier-5
// fuzzy-review fallback (dedup.go) filters events by fetch_source_id, but
// that column had no index — a full table scan on every dedup-tier-1-4 miss
// during import. Verifies the index exists both on a fresh install
// (createTables) and after migrating an existing DB (migrateDB, run twice
// to confirm idempotency).
func TestMigrationV36FetchSourceIDIndex(t *testing.T) {
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	old := db
	db = conn
	t.Cleanup(func() { db = old })

	if err := createTables(); err != nil {
		t.Fatalf("createTables: %v", err)
	}
	migrateDB()
	migrateDB() // idempotency on a second boot

	var n int
	conn.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_events_fetch_source_id'").Scan(&n)
	if n == 0 {
		t.Fatal("idx_events_fetch_source_id missing after createTables+migrateDB")
	}
}
