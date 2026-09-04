package main

import (
	"net/http/httptest"
	"strconv"
	"testing"
)

// TestGetEventSetsETagFromChangedAt covers #1248: getEvent used to make a
// redundant second query for changed_at to build its ETag, after
// scanEventRow had already read the same column. Now it reads
// event.ChangedAtEpoch, set unconditionally by scanEventRow — verify the
// header still reflects the real changed_at value.
func TestGetEventSetsETagFromChangedAt(t *testing.T) {
	setupDedupTestDB(t)

	eventID, _, _, err := insertEvent(db, EventInput{
		Title: "Session", StartTime: 2000000000, EndTime: 2000003600, IsPublished: true,
	})
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := db.Exec("UPDATE events SET changed_at=?, email_verified=1 WHERE id=?", 1234567890, eventID); err != nil {
		t.Fatalf("set changed_at: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/events/"+strconv.Itoa(eventID), nil)
	req.SetPathValue("id", strconv.Itoa(eventID))
	w := httptest.NewRecorder()
	getEvent(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if got, want := w.Header().Get("ETag"), weakEtag(1234567890); got != want {
		t.Errorf("ETag = %q, want %q", got, want)
	}
}
