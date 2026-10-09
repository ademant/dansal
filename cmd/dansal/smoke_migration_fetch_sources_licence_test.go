package main

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// fetch_sources.licence/attribution/terms_url/opt_out (#1483, compliance G9
// phase-65) must exist on fresh installs (createTables) and survive running
// migrateDB twice (idempotency), with the documented empty/zero defaults.
func TestSmokeMigrationFetchSourcesLicence(t *testing.T) {
	old := db
	t.Cleanup(func() { db = old })
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	db = conn
	if err := createTables(); err != nil {
		t.Fatalf("createTables: %v", err)
	}
	migrateDB()
	migrateDB()
	for _, col := range []string{"licence", "attribution", "terms_url", "opt_out"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('fetch_sources') WHERE name=?", col).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("column %s: want 1 got %d", col, n)
		}
	}
	res, err := db.Exec("INSERT INTO fetch_sources (url, type) VALUES ('https://example.test/feed', 'ical')")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	id, _ := res.LastInsertId()
	var licence, attribution, termsURL string
	var optOut int
	if err := db.QueryRow("SELECT licence, attribution, terms_url, opt_out FROM fetch_sources WHERE id=?", id).Scan(&licence, &attribution, &termsURL, &optOut); err != nil {
		t.Fatalf("select: %v", err)
	}
	if licence != "" || attribution != "" || termsURL != "" || optOut != 0 {
		t.Errorf("defaults = %q %q %q %d, want all empty/zero", licence, attribution, termsURL, optOut)
	}
}
