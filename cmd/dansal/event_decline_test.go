package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #1463: declining a suggested event must carry a reason (DSA Art. 17
// statement of reasons, #1442) and must only touch unpublished suggestions.
func TestDeclineSuggestedEventRequiresReason(t *testing.T) {
	setupDedupTestDB(t)
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (1, 'admin@example.test', 'Admin', 'admin')")
	db.Exec("INSERT INTO events (id, title, start_time, end_time, is_published) VALUES (10, 'Suggested', 2000000000, 2000003600, 0)")

	w := httptest.NewRecorder()
	declineEventHandler(w, adminDeclineRequest(10, `{}`))
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400 (body=%s)", w.Code, w.Body.String())
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM events WHERE id=10").Scan(&n)
	if n != 1 {
		t.Errorf("event was deleted despite missing reason")
	}
}

func TestDeclineSuggestedEventDeletes(t *testing.T) {
	setupDedupTestDB(t)
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (1, 'admin@example.test', 'Admin', 'admin')")
	db.Exec("INSERT INTO events (id, title, start_time, end_time, is_published) VALUES (10, 'Suggested', 2000000000, 2000003600, 0)")

	w := httptest.NewRecorder()
	declineEventHandler(w, adminDeclineRequest(10, `{"reason":"not in the focus of this instance"}`))
	if w.Code != 204 {
		t.Fatalf("status = %d, want 204 (body=%s)", w.Code, w.Body.String())
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM events WHERE id=10").Scan(&n)
	if n != 0 {
		t.Errorf("event not deleted")
	}
}

func TestDeclineRejectsPublishedEvent(t *testing.T) {
	setupDedupTestDB(t)
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (1, 'admin@example.test', 'Admin', 'admin')")
	db.Exec("INSERT INTO events (id, title, start_time, end_time, is_published) VALUES (10, 'Live', 2000000000, 2000003600, 1)")

	w := httptest.NewRecorder()
	declineEventHandler(w, adminDeclineRequest(10, `{"reason":"not in the focus of this instance"}`))
	if w.Code != 409 {
		t.Fatalf("status = %d, want 409 (body=%s)", w.Code, w.Body.String())
	}
}

func TestRejectPendingEditRequiresReason(t *testing.T) {
	setupDedupTestDB(t)
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (1, 'admin@example.test', 'Admin', 'admin')")
	db.Exec("INSERT INTO events (id, title, start_time, end_time, is_published, pending_edit_json, pending_edit_submitted_at) VALUES (20, 'Edited', 2000000000, 2000003600, 1, '{\"title\":\"x\"}', 1)")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/events/20/pending-edit/reject", strings.NewReader(`{}`))
	req.SetPathValue("id", "20")
	req.Header.Set("X-User-ID", "1")
	req.Header.Set("X-User-Role", RoleAdmin)
	w := httptest.NewRecorder()
	rejectPendingEdit(w, req)
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400 (body=%s)", w.Code, w.Body.String())
	}
	var pending string
	db.QueryRow("SELECT COALESCE(pending_edit_json,'') FROM events WHERE id=20").Scan(&pending)
	if pending == "" {
		t.Errorf("pending_edit_json was cleared despite missing reason")
	}
}

func adminDeclineRequest(id int, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/events/%d/decline", id), strings.NewReader(body))
	req.SetPathValue("id", fmt.Sprintf("%d", id))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", "1")
	req.Header.Set("X-User-Role", RoleAdmin)
	return req
}