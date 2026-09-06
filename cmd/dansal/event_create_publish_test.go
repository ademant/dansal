package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// #1254: the admin create form's "Published" checkbox used to have no
// effect — createEvent always published a new event for any authenticated
// caller. These tests protect the fix: an explicit is_published in the
// request body now controls it, while omitting the field entirely (every
// existing integration that predates this — feed imports, wp-dansal, etc.)
// must keep getting the old always-published default.
func TestCreateEventHonorsExplicitIsPublishedFalse(t *testing.T) {
	setupDedupTestDB(t)
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (1, 'admin@example.test', 'Admin', 'admin')")

	body := `{"title":"Draft via API","start_time":"2033-06-01T20:00:00Z","end_time":"2033-06-01T23:00:00Z","is_published":false}`
	req := httptest.NewRequest("POST", "/api/v1/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", "1")
	req.Header.Set("X-User-Role", RoleAdmin)
	w := httptest.NewRecorder()
	createEvent(w, req)

	if w.Code != 201 && w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var created []Event
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v, body=%s", err, w.Body.String())
	}
	if len(created) != 1 {
		t.Fatalf("got %d events, want 1", len(created))
	}
	if created[0].IsPublished {
		t.Errorf("IsPublished = true, want false (explicit is_published:false must be honored)")
	}
}

func TestCreateEventDefaultsToPublishedWhenOmitted(t *testing.T) {
	setupDedupTestDB(t)
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (1, 'admin@example.test', 'Admin', 'admin')")

	body := `{"title":"No publish field","start_time":"2033-06-02T20:00:00Z","end_time":"2033-06-02T23:00:00Z"}`
	req := httptest.NewRequest("POST", "/api/v1/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", "1")
	req.Header.Set("X-User-Role", RoleAdmin)
	w := httptest.NewRecorder()
	createEvent(w, req)

	if w.Code != 201 && w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var created []Event
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v, body=%s", err, w.Body.String())
	}
	if len(created) != 1 {
		t.Fatalf("got %d events, want 1", len(created))
	}
	if !created[0].IsPublished {
		t.Errorf("IsPublished = false, want true (omitting is_published must keep the pre-#1254 default)")
	}
}
