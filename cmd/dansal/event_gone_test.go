package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// getEventAnon issues an anonymous GET /api/v1/events/{id}.
func getEventAnon(t *testing.T, id int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events/"+strconv.Itoa(id), nil)
	req.SetPathValue("id", strconv.Itoa(id))
	w := httptest.NewRecorder()
	getEvent(w, req)
	return w
}

// TestGetEventAnonymousStatusCodes covers #1634: an anonymous GET must
// answer 404 for an existing unpublished draft (it may be published later),
// 410 only for an id that was deleted, and 404 for an id never allocated.
func TestGetEventAnonymousStatusCodes(t *testing.T) {
	setupDedupTestDB(t)

	const start = 2000000000
	published, _, _, err := insertEvent(db, EventInput{
		Title: "Public", StartTime: start, EndTime: start + 3600, IsPublished: true,
	})
	if err != nil {
		t.Fatalf("insert published event: %v", err)
	}
	draft, _, _, err := insertEvent(db, EventInput{
		Title: "Draft", StartTime: start, EndTime: start + 3600, IsPublished: false,
	})
	if err != nil {
		t.Fatalf("insert draft event: %v", err)
	}
	deleted, _, _, err := insertEvent(db, EventInput{
		Title: "Deleted", StartTime: start, EndTime: start + 3600, IsPublished: true,
	})
	if err != nil {
		t.Fatalf("insert event to delete: %v", err)
	}
	if _, err := db.Exec("DELETE FROM events WHERE id = ?", deleted); err != nil {
		t.Fatalf("hard-delete event: %v", err)
	}

	tests := []struct {
		name string
		id   int
		want int
	}{
		{"published event is visible", published, http.StatusOK},
		{"unpublished draft is not gone", draft, http.StatusNotFound},
		{"hard-deleted event is gone", deleted, http.StatusGone},
		{"never-allocated id is not found", deleted + 1000, http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if w := getEventAnon(t, tc.id); w.Code != tc.want {
				t.Fatalf("GET /events/%d: status = %d, want %d, body=%s", tc.id, w.Code, tc.want, w.Body.String())
			}
		})
	}
}
