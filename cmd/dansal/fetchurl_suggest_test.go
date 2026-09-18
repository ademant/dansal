package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// setupFetchSuggestTestDB mirrors suggest_test.go's shared-cache setup so the
// handler's background notification goroutine sees the same in-memory DB.
func setupFetchSuggestTestDB(t *testing.T) {
	t.Helper()
	old := db
	t.Cleanup(func() { db = old })

	conn, err := sql.Open("sqlite3", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	db = conn

	if err := createTables(); err != nil {
		t.Fatalf("createTables: %v", err)
	}
	migrateDB()

	oldCfg := config
	config = &Config{}
	config.Server.MaxOpenTokensPerAddress = 5
	t.Cleanup(func() { config = oldCfg })

	fetchSuggestRateLimiter = NewRateLimiter(1000, time.Minute)
	fetchSuggestPreviewRateLimiter = NewRateLimiter(1000, time.Minute)
}

func postFetchSuggest(req FetchSuggestRequest) *httptest.ResponseRecorder {
	body, _ := json.Marshal(req)
	httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/fetchurl/suggest", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	fetchSuggestHandler(rec, httpReq)
	return rec
}

// TestFetchSuggestHandlerHoneypot guards the anti-bot honeypot: a filled-in
// phone2 field must be silently accepted (202) without ever touching the
// database or the network, exactly like suggestHandler's own honeypot (#1333).
func TestFetchSuggestHandlerHoneypot(t *testing.T) {
	setupFetchSuggestTestDB(t)

	rec := postFetchSuggest(FetchSuggestRequest{
		Email:   "bot@example.com",
		Phone2:  "5551234", // honeypot filled in
		FeedURL: "https://example.com/calendar.ics",
		OrgName: "Bot Org",
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM pending_fetch_suggestions").Scan(&n)
	if n != 0 {
		t.Fatalf("honeypot submission was persisted: %d rows", n)
	}
}

// TestFetchSuggestHandlerRequiresOrgChoice guards the "exactly one of org_id
// or org_name" rule: neither given, or both given, must both be rejected
// before any network fetch happens.
func TestFetchSuggestHandlerRequiresOrgChoice(t *testing.T) {
	setupFetchSuggestTestDB(t)

	rec := postFetchSuggest(FetchSuggestRequest{
		Email:   "person@example.com",
		FeedURL: "https://example.com/calendar.ics",
		// no OrgID, no OrgName
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing org: status=%d body=%s", rec.Code, rec.Body.String())
	}

	orgID := 1
	rec = postFetchSuggest(FetchSuggestRequest{
		Email:   "person@example.com",
		FeedURL: "https://example.com/calendar.ics",
		OrgID:   &orgID,
		OrgName: "Also a new org",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("both org_id and org_name: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestFetchSuggestHandlerRejectsUnknownOrgID guards against a client
// submitting org_id for an organization that doesn't exist.
func TestFetchSuggestHandlerRejectsUnknownOrgID(t *testing.T) {
	setupFetchSuggestTestDB(t)

	missingOrgID := 999999
	rec := postFetchSuggest(FetchSuggestRequest{
		Email:   "person@example.com",
		FeedURL: "https://example.com/calendar.ics",
		OrgID:   &missingOrgID,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestFetchSuggestHandlerRejectsInvalidEmail guards the email-as-signature
// requirement: this feature always requires a valid email, unlike
// suggestHandler which only requires it when SMTP is configured — a
// suggestion is a standing recurring import commitment, not a one-off event.
func TestFetchSuggestHandlerRejectsInvalidEmail(t *testing.T) {
	setupFetchSuggestTestDB(t)

	rec := postFetchSuggest(FetchSuggestRequest{
		Email:   "not-an-email",
		FeedURL: "https://example.com/calendar.ics",
		OrgName: "Some Org",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var n int
	db.QueryRow("SELECT COUNT(*) FROM pending_fetch_suggestions").Scan(&n)
	if n != 0 {
		t.Fatalf("invalid-email submission was persisted: %d rows", n)
	}
}
