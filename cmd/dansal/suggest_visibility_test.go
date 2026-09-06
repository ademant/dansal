package main

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"
)

// #1272: email confirmation stopped being a gate on anything — is_published
// is now the sole visibility rule, and admins see unpublished suggestions
// regardless of whether they were ever confirmed. These tests protect that
// against a regression back to the old "WHERE e.email_verified = 1" default
// that used to hide unconfirmed suggestions from admins entirely (and would
// have hidden a published-but-unconfirmed event from everyone else too).
func TestGetEventsAdminSeesUnpublishedRegardlessOfEmailVerified(t *testing.T) {
	setupDedupTestDB(t)

	mustInsertSuggestion(t, "Verified but unpublished", true, false)
	mustInsertSuggestion(t, "Unverified and unpublished", false, false)

	req := httptest.NewRequest("GET", "/api/v1/events?unpublished=1&include_past=true&limit=100", nil)
	req.Header.Set("X-User-ID", "1")
	req.Header.Set("X-User-Role", RoleAdmin)
	w := httptest.NewRecorder()
	getEvents(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var events []Event
	if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil {
		t.Fatalf("decode: %v, body=%s", err, w.Body.String())
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (both unpublished suggestions should be visible to admin regardless of email_verified); body=%s", len(events), w.Body.String())
	}
}

// The ?email_verified= param must still work as an explicit narrowing
// filter for admins, even though it's no longer the default restriction.
func TestGetEventsEmailVerifiedFilterStillNarrows(t *testing.T) {
	setupDedupTestDB(t)

	mustInsertSuggestion(t, "Verified", true, false)
	mustInsertSuggestion(t, "Unverified", false, false)

	req := httptest.NewRequest("GET", "/api/v1/events?email_verified=false&include_past=true&limit=100", nil)
	req.Header.Set("X-User-ID", "1")
	req.Header.Set("X-User-Role", RoleAdmin)
	w := httptest.NewRecorder()
	getEvents(w, req)

	var events []Event
	if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil {
		t.Fatalf("decode: %v, body=%s", err, w.Body.String())
	}
	if len(events) != 1 || events[0].Title != "Unverified" {
		t.Fatalf("email_verified=false filter: got %+v, want exactly the unverified event", events)
	}
}

// A published event must be publicly visible (by short_code and by id) even
// if it was never confirmed by the suggester — is_published is the only
// gate now.
func TestPublishedEventVisiblePubliclyRegardlessOfEmailVerified(t *testing.T) {
	setupDedupTestDB(t)

	id := mustInsertSuggestion(t, "Published, never confirmed", false, true)
	var shortCode string
	if err := db.QueryRow("SELECT short_code FROM events WHERE id = ?", id).Scan(&shortCode); err != nil {
		t.Fatalf("read short_code: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/events?code="+shortCode, nil)
	w := httptest.NewRecorder()
	getEvents(w, req)
	if w.Code != 200 {
		t.Fatalf("short_code lookup: status = %d, body=%s", w.Code, w.Body.String())
	}

	idStr := strconv.FormatInt(id, 10)
	req2 := httptest.NewRequest("GET", "/api/v1/events/"+idStr, nil)
	req2.SetPathValue("id", idStr)
	w2 := httptest.NewRecorder()
	getEvent(w2, req2)
	if w2.Code != 200 {
		t.Fatalf("get-by-id: status = %d, body=%s", w2.Code, w2.Body.String())
	}
}

var mustInsertSuggestionCounter int

// mustInsertSuggestion inserts a minimal suggestion-shaped event row directly
// (bypassing suggestSubmitHandler's SMTP/turnstile/rate-limit plumbing, which
// isn't what these tests are about) and returns its id.
func mustInsertSuggestion(t *testing.T, title string, emailVerified, isPublished bool) int64 {
	t.Helper()
	mustInsertSuggestionCounter++
	shortCode := "sug" + strconv.Itoa(mustInsertSuggestionCounter)
	res, err := db.Exec(
		`INSERT INTO events (title, description, start_time, end_time, is_published, email_verified, suggester_email, short_code)
		 VALUES (?, '', 2000000000, 2000003600, ?, ?, 'suggester@example.com', ?)`,
		title, isPublished, emailVerified, shortCode,
	)
	if err != nil {
		t.Fatalf("insert suggestion %q: %v", title, err)
	}
	id, _ := res.LastInsertId()
	return id
}
