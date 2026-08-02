package main

import (
	"bytes"
	"database/sql"
	"log"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestFilterKnownTags(t *testing.T) {
	old := db
	defer func() { db = old }()

	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	db = conn

	if err := createTables(); err != nil {
		t.Fatalf("createTables: %v", err)
	}
	migrateDB()

	// bal-folk and festival are part of the seeded tags vocabulary (see
	// CLAUDE.md); Termine/Aktuelles mimic a feed's own unrelated taxonomy
	// (e.g. balfolk-halle.de's Atom <category> terms).
	got := filterKnownTags([]string{"bal-folk", "Termine", "festival", "Aktuelles"})
	want := []string{"bal-folk", "festival"}
	if len(got) != len(want) {
		t.Fatalf("filterKnownTags() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("filterKnownTags() = %v, want %v", got, want)
		}
	}
}

// TestWithEntrySavepointIsolatesFailure verifies that an error from one
// entry's closure rolls back only that entry's writes via SAVEPOINT, while
// the surrounding transaction remains usable for subsequent entries and
// still commits (see #923: one bad entry must not fail the whole feed).
func TestWithEntrySavepointIsolatesFailure(t *testing.T) {
	old := db
	defer func() { db = old }()

	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	db = conn

	if _, err := db.Exec(`CREATE TABLE probe (val TEXT)`); err != nil {
		t.Fatal(err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}

	if err := withEntrySavepoint(tx, func() error {
		_, err := tx.Exec(`INSERT INTO probe (val) VALUES ('ok1')`)
		return err
	}); err != nil {
		t.Fatalf("entry 1 (should succeed): %v", err)
	}

	if err := withEntrySavepoint(tx, func() error {
		if _, err := tx.Exec(`INSERT INTO probe (val) VALUES ('bad')`); err != nil {
			return err
		}
		return sql.ErrNoRows // simulate the entry failing after a partial write
	}); err == nil {
		t.Fatal("entry 2 (should fail): got nil error")
	}

	if err := withEntrySavepoint(tx, func() error {
		_, err := tx.Exec(`INSERT INTO probe (val) VALUES ('ok2')`)
		return err
	}); err != nil {
		t.Fatalf("entry 3 (should succeed): %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	rows, err := db.Query(`SELECT val FROM probe ORDER BY val`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var v string
		rows.Scan(&v)
		got = append(got, v)
	}
	want := []string{"ok1", "ok2"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("probe rows = %v, want %v (bad entry's insert must have rolled back)", got, want)
	}
}

// TestLogFailedImportEntry verifies the always-on log line names the failed
// entry, and that the full request is only dumped when Server.Debug is set.
func TestLogFailedImportEntry(t *testing.T) {
	oldCfg := config
	defer func() { config = oldCfg }()

	src := FetchSource{URL: "https://example.test/feed.atom"}
	req := EventCreateRequest{
		UID: "abc123",
		EventWriteRequest: EventWriteRequest{
			Title: "Bad Entry",
		},
	}

	t.Run("debug off: no full dump", func(t *testing.T) {
		config = &Config{}
		var buf bytes.Buffer
		orig := log.Writer()
		log.SetOutput(&buf)
		defer log.SetOutput(orig)

		logFailedImportEntry(src, req, sql.ErrNoRows)
		out := buf.String()
		if !strings.Contains(out, "Bad Entry") || !strings.Contains(out, "abc123") {
			t.Fatalf("expected summary line with title/uid, got: %s", out)
		}
		if strings.Contains(out, "\"uid\":\"abc123\"") {
			t.Fatalf("did not expect JSON dump when Debug is off, got: %s", out)
		}
	})

	t.Run("debug on: full request dumped", func(t *testing.T) {
		config = &Config{Server: ServerConfig{Debug: true}}
		var buf bytes.Buffer
		orig := log.Writer()
		log.SetOutput(&buf)
		defer log.SetOutput(orig)

		logFailedImportEntry(src, req, sql.ErrNoRows)
		out := buf.String()
		if !strings.Contains(out, `"uid":"abc123"`) {
			t.Fatalf("expected JSON dump of failed entry when Debug is on, got: %s", out)
		}
	})
}
