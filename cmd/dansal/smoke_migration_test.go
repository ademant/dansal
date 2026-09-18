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
