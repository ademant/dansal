package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// TestSuggestVerifyHandlerIdempotent guards against #1151 regressing: the
// verify token is a standing link (#928) that's never destroyed, so a
// suggester can revisit it long after the event was already verified. The
// handler must still flip email_verified on the genuine first visit, but a
// repeat visit must find it already set and not re-run the "new suggestion"
// notification path.
func TestSuggestVerifyHandlerIdempotent(t *testing.T) {
	old := db
	defer func() { db = old }()

	// The handler fires notifyAdminsSuggestion in a background goroutine, so
	// the pool needs a second connection to see the same database. A plain
	// ":memory:" DSN hands out a fresh, empty database per connection ("no
	// such table: users"); pinning the pool to one connection instead
	// deadlocks against createTables()/migrateDB()'s own nested queries.
	// A shared-cache DSN keeps every pooled connection on the same database.
	conn, err := sql.Open("sqlite3", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	db = conn

	if err := createTables(); err != nil {
		t.Fatalf("createTables: %v", err)
	}
	migrateDB()

	res, err := db.Exec(`INSERT INTO events (title, start_time, end_time, suggestion_token, suggestion_token_expires_at, email_verified)
		VALUES ('Test suggestion', 1893456000, 1893459600, 'tok123', 4102444800, 0)`)
	if err != nil {
		t.Fatal(err)
	}
	eventID, _ := res.LastInsertId()

	callVerify := func() int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/events/suggest/verify/tok123", nil)
		req.SetPathValue("token", "tok123")
		rec := httptest.NewRecorder()
		suggestVerifyHandler(rec, req)
		return rec.Code
	}

	verifiedAfter := func() bool {
		var v bool
		if err := db.QueryRow(`SELECT email_verified FROM events WHERE id = ?`, eventID).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	if code := callVerify(); code != http.StatusOK {
		t.Fatalf("first verify: status=%d", code)
	}
	if !verifiedAfter() {
		t.Fatal("first verify: email_verified was not set to true")
	}

	// Repeat visit to the same standing link: must remain a no-op success,
	// not error, and must not flip anything back.
	if code := callVerify(); code != http.StatusOK {
		t.Fatalf("second verify: status=%d", code)
	}
	if !verifiedAfter() {
		t.Fatal("second verify: email_verified was unexpectedly reset")
	}
}
